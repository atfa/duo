package session

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// Resume rebuilds the visible history from the journal (resume.go reads
// TUIEntries) and replays reconciliation and delivery. A session can be resumed
// more than once — the second `duo resume`, a retry after a crash, a frontend
// reopened — so replaying the same journal must not duplicate entries, re-emit
// effects that already happened, or move Git again.
func TestResumeJournalReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := gitRepo(t)
	baseDir := t.TempDir()
	envDir := t.TempDir()
	script := writeFakeBridgeAgent(t, envDir)
	const id = "e2e-journal"

	opts := Options{
		LaunchDir:          repo,
		BaseDir:            baseDir,
		Session:            id,
		Listen:             "127.0.0.1:0",
		Mode:               project.ModeFast,
		DefaultAgentDriver: "stub",
		HarnessEnabled:     false,
		RegisterDrivers:    registerJournalStub(script),
	}
	svc := New(opts)
	if _, err := svc.Start(ctx); err != nil {
		t.Fatalf("start session: %v", err)
	}
	round := &e2eRound{svc: svc, repo: repo, baseDir: baseDir, envDir: envDir, session: id}
	t.Cleanup(func() {
		for _, agent := range round.agents {
			_ = agent.Close()
		}
		svc.Close()
	})

	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	delivered := commitInWorktree(t, svc.View().Worktrees.Austin.Path, "result.md", "hello Duo\n", "add result.md")
	austin.sign(t)
	round.waitPhase(t, project.PhaseVerify)
	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "looks right"})
	round.waitPhase(t, project.PhaseDone)
	if !svc.View().Delivery.Applied() {
		t.Fatalf("delivery = %+v, want the round to deliver", svc.View().Delivery)
	}
	svc.Close()

	// The transcript a reviewer wrote before the first resume. It is the part of
	// the journal a resume reads back, so duplicating it would duplicate history.
	store := openSessionStore(t, baseDir, repo, id)
	journal := store.OpenEvents()
	panes := []sessionstore.TUIEntry{
		{Pane: "Austin", Text: "journal replay: first entry"},
		{Pane: "Tony", Text: "journal replay: second entry"},
		{Pane: "Duo", Text: "journal replay: third entry"},
	}
	for _, entry := range panes {
		journal.RecordTUIEntry(entry)
	}

	before, err := store.Load()
	if err != nil {
		t.Fatalf("load the finished session: %v", err)
	}
	if before.Phase != string(project.PhaseDone) || !before.Delivery.Applied() {
		t.Fatalf("the finished session is not durable: phase %s delivery %+v", before.Phase, before.Delivery)
	}
	if got := headOf(t, repo); got != delivered {
		t.Fatalf("user repository = %s, want the delivered head %s", got, delivered)
	}
	eventsBefore := journalEventCounts(t, store)

	var histories [][]sessionstore.TUIEntry
	for attempt := 1; attempt <= 2; attempt++ {
		resumed := New(Options{
			LaunchDir:          repo,
			BaseDir:            baseDir,
			Session:            id,
			Listen:             "127.0.0.1:0",
			Mode:               project.ModeFast,
			DefaultAgentDriver: "stub",
			HarnessEnabled:     false,
			Resume:             true,
			ResumeSession:      id,
			RegisterDrivers:    registerJournalStub(script),
		})
		if _, err := resumed.Start(ctx); err != nil {
			t.Fatalf("resume %d: %v", attempt, err)
		}
		histories = append(histories, resumed.History())
		view := resumed.View()
		resumed.Close()

		if view.Project.Phase != project.PhaseDone {
			t.Fatalf("resume %d left the session in %s, want DONE", attempt, view.Project.Phase)
		}
		if !view.Delivery.Applied() || view.Delivery.AppliedHead != delivered {
			t.Fatalf("resume %d rewrote the delivery record: %+v", attempt, view.Delivery)
		}
		if view.Delivery.CompletedAt == nil || before.Delivery.CompletedAt == nil || !view.Delivery.CompletedAt.Equal(*before.Delivery.CompletedAt) {
			t.Fatalf("resume %d re-stamped the delivery: %+v", attempt, view.Delivery)
		}

		after, err := store.Load()
		if err != nil {
			t.Fatalf("load after resume %d: %v", attempt, err)
		}
		assertDeliveryReplayStable(t, before, after, attempt)
		if got := headOf(t, repo); got != delivered {
			t.Fatalf("resume %d moved the user repository to %s, want %s", attempt, got, delivered)
		}
	}

	// Reading the same journal twice yields the same history, and each entry is
	// replayed exactly once.
	if !reflect.DeepEqual(histories[0], histories[1]) {
		t.Fatalf("the second replay produced a different history:\nfirst  %+v\nsecond %+v", histories[0], histories[1])
	}
	for _, want := range panes {
		count := 0
		for _, entry := range histories[1] {
			if entry.Pane == want.Pane && entry.Text == want.Text {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("entry %q is in the replayed history %d times, want once", want.Text, count)
		}
	}

	eventsAfter := journalEventCounts(t, store)
	for _, kind := range []string{"session_delivered", "step_landed", "tui_entry"} {
		if eventsAfter[kind] != eventsBefore[kind] {
			t.Fatalf("resuming re-emitted %s records: %d -> %d", kind, eventsBefore[kind], eventsAfter[kind])
		}
	}
	if got := eventsAfter["session_resume"] - eventsBefore["session_resume"]; got != 2 {
		t.Fatalf("session_resume records grew by %d, want one per resume", got)
	}
}

// registerJournalStub registers the in-process driver these resumes reuse, with
// the agent command the first round was launched with.
func registerJournalStub(script string) func() {
	return func() {
		driver.Register("stub", func() driver.Handler {
			return agenttest.New("stub").With(func(spec *agenttest.Spec) {
				spec.Command = script
			})
		})
	}
}

// assertDeliveryReplayStable pins the fields a replay must not rewrite: the
// reconciled workflow and the delivery/verification records.
func assertDeliveryReplayStable(t *testing.T, before, after sessionstore.Snapshot, attempt int) {
	t.Helper()
	if after.Phase != before.Phase || after.Mode != before.Mode {
		t.Fatalf("resume %d changed the workflow: %s/%s -> %s/%s", attempt, before.Phase, before.Mode, after.Phase, after.Mode)
	}
	if !reflect.DeepEqual(after.Delivery, before.Delivery) {
		t.Fatalf("resume %d changed the delivery record:\nbefore %+v\nafter  %+v", attempt, before.Delivery, after.Delivery)
	}
	if !reflect.DeepEqual(after.Verification, before.Verification) {
		t.Fatalf("resume %d changed the verification record:\nbefore %+v\nafter  %+v", attempt, before.Verification, after.Verification)
	}
	if !reflect.DeepEqual(after.Ready, before.Ready) || !reflect.DeepEqual(after.Evidence, before.Evidence) {
		t.Fatalf("resume %d changed the signatures: ready %v -> %v, evidence %v -> %v",
			attempt, before.Ready, after.Ready, before.Evidence, after.Evidence)
	}
}

// journalEventCounts counts journal records by type, which is how a replay that
// re-emits an effect instead of re-reading it becomes visible.
func journalEventCounts(t *testing.T, store *sessionstore.Store) map[string]int {
	t.Helper()
	file, err := os.Open(store.OpenEvents().Path())
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer file.Close()

	counts := map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event sessionstore.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("journal line is not an event: %v", err)
		}
		counts[event.Type]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return counts
}

// TestRecentActivitySeededFromTheResumeJournal proves a resumed session's first
// RecentActivity already holds the recorded lines, without waiting for new
// activity: the ring starts from the journal the resume restored. The session
// must actually start its workflow, because Close discards the record of a run
// that never began.
func TestRecentActivitySeededFromTheResumeJournal(t *testing.T) {
	ctx := context.Background()
	repo := gitRepo(t)
	baseDir := t.TempDir()
	envDir := t.TempDir()
	script := writeFakeBridgeAgent(t, envDir)
	const id = "e2e-recent-activity"

	opts := Options{
		LaunchDir:          repo,
		BaseDir:            baseDir,
		Session:            id,
		Listen:             "127.0.0.1:0",
		Mode:               project.ModeFast,
		DefaultAgentDriver: "stub",
		HarnessEnabled:     false,
		RegisterDrivers:    registerJournalStub(script),
	}
	svc := New(opts)
	if _, err := svc.Start(ctx); err != nil {
		t.Fatalf("start session: %v", err)
	}
	round := &e2eRound{svc: svc, repo: repo, baseDir: baseDir, envDir: envDir, session: id}
	t.Cleanup(func() {
		for _, agent := range round.agents {
			_ = agent.Close()
		}
		svc.Close()
	})

	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	delivered := commitInWorktree(t, svc.View().Worktrees.Austin.Path, "result.md", "hello Duo\n", "add result.md")
	if delivered == "" {
		t.Fatal("the worktree commit produced no head")
	}
	austin.sign(t)
	round.waitPhase(t, project.PhaseVerify)
	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "looks right"})
	round.waitPhase(t, project.PhaseDone)
	svc.RecordEntry(sessionstore.TUIEntry{Pane: "Duo", Text: "seeded: before close"})
	svc.Close()

	resumed := New(Options{
		LaunchDir:          repo,
		BaseDir:            baseDir,
		Session:            id,
		Listen:             "127.0.0.1:0",
		Mode:               project.ModeFast,
		DefaultAgentDriver: "stub",
		HarnessEnabled:     false,
		Resume:             true,
		ResumeSession:      id,
		RegisterDrivers:    registerJournalStub(script),
	})
	if _, err := resumed.Start(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.Close()

	got := resumed.RecentActivity(50)
	found := false
	for _, entry := range got {
		if entry.Text == "seeded: before close" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resumed RecentActivity is missing the recorded entry; got %d entries", len(got))
	}
}
