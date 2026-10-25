package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// TestSnapshotLiveBlockCarriesGodViewFacts pins the facts only Duo can see: who
// has signed, what each branch actually holds, which files both branches changed,
// and the verdict bound to a head. Without them a report can only guess why the
// work stopped, and the Watch out section would have nothing to cite.
func TestSnapshotLiveBlockCarriesGodViewFacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeStep)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Touch one shared file from both branches"})
	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add shared.md"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	set := round.svc.View().Worktrees
	commitInWorktree(t, set.Austin.Path, "shared.md", "austin\n", "austin: shared.md")
	commitInWorktree(t, set.Austin.Path, "austin-only.md", "austin\n", "austin: own file")
	commitInWorktree(t, set.Tony.Path, "shared.md", "tony\n", "tony: shared.md")
	commitInWorktree(t, set.Tony.Path, "tony-only.md", "tony\n", "tony: own file")
	// An uncommitted edit is the evidence that a claim is still only local work.
	writeFile(t, filepath.Join(set.Tony.Path, "tony-only.md"), "tony, uncommitted\n")

	live := round.svc.observerLiveContext()
	for _, want := range []string{
		"Sign-offs: Austin ready=",
		", Tony ready=",
		"Worktrees: Austin head=",
		"ahead=2 dirty=false",
		"Tony head=",
		"ahead=2 dirty=true",
		"Changed files vs base: Austin [austin-only.md, shared.md] (2 file(s))",
		"Tony [shared.md, tony-only.md] (2 file(s))",		"overlap [shared.md] (1 file(s))",
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live block missing %q:\n%s", want, live)
		}
	}
	if strings.Contains(live, "Verification:") {
		t.Errorf("an unrequested verification was reported:\n%s", live)
	}
}

// TestSnapshotLiveBlockNamesTheVerificationTarget checks the Fast-mode record:
// the verdict alone is not enough, because a passed verdict for an older head is
// exactly the state a human needs to see.
func TestSnapshotLiveBlockNamesTheVerificationTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeFast)
	round.dial(t, protocol.Austin)
	round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austinPath := round.svc.View().Worktrees.Austin.Path
	head := commitInWorktree(t, austinPath, "done.md", "done\n", "implement the task")

	live := round.svc.observerLiveContext()
	if strings.Contains(live, "Sign-offs:") {
		t.Fatalf("Fast mode has no dual sign-off to report:\n%s", live)
	}
	if !strings.Contains(live, "ahead=1") {
		t.Fatalf("live block missing the branch commit count:\n%s", live)
	}
	if !strings.Contains(live, "Changed files vs base: Austin [done.md] (1 file(s))") {
		t.Fatalf("live block missing the branch's changed file:\n%s", live)
	}
	if head == "" {
		t.Fatal("no head recorded")
	}
}

// TestLiveChangeLineNeverCallsAnUnreadableBranchEmpty: a branch git could not
// describe is left out, and the overlap is only claimed when both sides are known,
// so a report can never read a failed git read as "this branch changed nothing".
func TestLiveChangeLineNeverCallsAnUnreadableBranchEmpty(t *testing.T) {
	austin := observerBranch{ok: true, files: []string{"shared.md"}}
	if got := changeLine(austin, observerBranch{}); got != "Changed files vs base: Austin [shared.md] (1 file(s))" {
		t.Fatalf("unknown Tony branch = %q", got)
	}
	if got := changeLine(observerBranch{}, observerBranch{}); got != "" {
		t.Fatalf("two unknown branches = %q, want no line", got)
	}
	got := changeLine(austin, observerBranch{ok: true, files: []string{"shared.md"}})
	if want := "Changed files vs base: Austin [shared.md] (1 file(s)); Tony [shared.md] (1 file(s)); overlap [shared.md] (1 file(s))"; got != want {
		t.Fatalf("known branches = %q, want %q", got, want)
	}
}

// TestLiveFileSetsBoundTheDisplayNotTheComparison: a branch with more files than
// the display bound still reports the real count, and a collision beyond the cut
// is still found — a hidden overlap would make the merge risk invisible.
func TestLiveFileSetsBoundTheDisplayNotTheComparison(t *testing.T) {
	many := make([]string, maxLiveBranchFiles+8)
	for i := range many {
		many[i] = fmt.Sprintf("file%02d.go", i)
	}
	text := fileSetText(many)
	if !strings.Contains(text, "+8 more") || !strings.Contains(text, fmt.Sprintf("(%d file(s))", len(many))) {
		t.Fatalf("bounded file set = %q", text)
	}
	if got := fileSetText(nil); got != "none" {
		t.Fatalf("empty file set = %q, want none", got)
	}
	last := many[len(many)-1]
	shared := overlappingFiles(many, []string{last, "other.go"})
	if len(shared) != 1 || shared[0] != last {
		t.Fatalf("overlap beyond the display cut = %v, want %q", shared, last)
	}
}

// TestVerificationLineAndUsageLineStayQuietWhenThereIsNothingToSay: an empty
// verdict and an unmeasured session must not add a line that reads as a fact.
func TestVerificationLineAndUsageLineStayQuietWhenThereIsNothingToSay(t *testing.T) {
	if got := verificationLine(project.Verification{}); got != "" {
		t.Fatalf("empty verification = %q, want no line", got)
	}
	got := verificationLine(project.Verification{Status: project.VerificationPassed, Head: "abcdef0123456", Note: "all\n green"})
	if want := "Verification: passed target=abcdef0 (note: all green)"; got != want {
		t.Fatalf("verification line = %q, want %q", got, want)
	}
	if got := usageLine(nil); got != "" {
		t.Fatalf("nil usage = %q, want no line", got)
	}
	ledger := &sessionstore.UsageLedger{Agents: map[protocol.AgentID]sessionstore.UsageTotals{
		protocol.Austin: {Turns: 3, TotalTokens: 12345, CostUSD: 0.42},
	}}
	if want := "Usage so far: 3 turn(s), 12.3k token(s), $0.42"; usageLine(ledger) != want {
		t.Fatalf("usage line = %q, want %q", usageLine(ledger), want)
	}
}

// TestSnapshotLiveBlockCarriesPlanAndSteps pins the input the Progress section
// of a report is written from: the live block has to carry the shared plan text
// and the step queue with its landed/submitted markers, because "how was the
// task decomposed and how far has it come" cannot be answered from the log.
func TestSnapshotLiveBlockCarriesPlanAndSteps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeStep)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Add two files, one step each"})
	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add first.md\n- [ ] add second.md"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	austinPath := round.svc.View().Worktrees.Austin.Path
	commitInWorktree(t, austinPath, "first.md", "first\n", "add first.md")
	austin.sign(t)
	round.waitPhase(t, project.PhaseReview)
	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	waitUntil(t, func() bool {
		snap := round.svc.View().Project
		return len(snap.Steps) == 2 && snap.Steps[0].Done && !snap.Steps[1].Done
	}, "step 1 to land")

	live := round.svc.observerLiveContext()
	for _, want := range []string{
		"mode=STEP phase=EXECUTE ",
		"step=2/2",
		"Shared plan (version 1):",
		"Add two files, one step each",
		"Step queue (2 step(s), 1 landed, current step 2 of 2):",
		"1. [x] [fast] add first.md — landed ",
		"2. [ ] [fast] add second.md",
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live block missing %q:\n%s", want, live)
		}
	}
}

// TestSnapshotLiveBlockAfterTheLastStepLands pins the queue tally at the moment
// a progress report is most likely opened: the last step has landed but the
// session is still in INTEGRATE, where Snapshot.Step is len+1 by contract. The
// header's step=3/2 is fine (it is the cursor), but the queue must not claim a
// "current step" that is past the end.
func TestSnapshotLiveBlockAfterTheLastStepLands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeStep)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "One step"})
	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add only.md"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	commitInWorktree(t, round.svc.View().Worktrees.Austin.Path, "only.md", "only\n", "add only.md")
	austin.sign(t)
	round.waitPhase(t, project.PhaseReview)
	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	// Land the last step: its dual INTEGRATE sign-off delivers and ticks the
	// queue, leaving the cursor at len+1 in INTEGRATE — the state the tally
	// below must survive.
	austin.sign(t)
	tony.sign(t)
	waitUntil(t, func() bool {
		snap := round.svc.View().Project
		return len(snap.Steps) == 1 && snap.Steps[0].Done && snap.Step == len(snap.Steps)+1
	}, "the only step to land with the cursor past the end")

	live := round.svc.observerLiveContext()
	if !strings.Contains(live, "Step queue (1 step(s), 1 landed, all steps landed):") {
		t.Fatalf("live block missing the all-landed tally:\n%s", live)
	}
	if strings.Contains(live, "current step") {
		t.Fatalf("all-landed queue must not name a current step:\n%s", live)
	}
}

// TestSnapshotLiveBlockCarriesPlanInGoalMode checks the Goal-mode half: with no
// step queue the live block still carries the plan text, so a report can state
// the task decomposition without inventing one.
func TestSnapshotLiveBlockCarriesPlanInGoalMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeGoal)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Redesign the scheduler and keep tests green"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	live := round.svc.observerLiveContext()
	if !strings.Contains(live, "Shared plan (version 1):\nRedesign the scheduler and keep tests green\n") {
		t.Fatalf("live block missing the goal-mode plan:\n%s", live)
	}
	if strings.Contains(live, "Step queue") {
		t.Fatalf("goal mode must not render a step queue:\n%s", live)
	}
}

// TestClipPlanNeverExceedsItsBound pins the fallback rule: a plan without
// newlines — the common goal-mode shape where the numbered items are inlined —
// is still cut at maxPlanRunes, and every cut keeps the truncation marker so a
// report cannot read a partial plan as the whole decomposition.
func TestClipPlanNeverExceedsItsBound(t *testing.T) {
	long := strings.Repeat("1) step one 2) step two 3) step three ", 300) // > 2× maxPlanRunes, one line
	got := clipPlan(long)
	if cut, ok := clipRunesMarked(long, maxPlanRunes); !ok || got != cut {
		t.Fatalf("a one-line plan must be cut at the bound with a marker, got %d runes", len([]rune(got)))
	}
	if strings.HasSuffix(got, "1)") || strings.HasSuffix(got, "1") {
		t.Fatalf("cut must not land mid-token without a marker: %q", clipRunes(got, 40))
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("the truncation marker is lost:\n%q", clipRunes(got, 80))
	}

	multi := "1) first item\n2) second item\n" + strings.Repeat("filler line\n", 500) + "3) third item"
	got = clipPlan(multi)
	if len([]rune(got)) > maxPlanRunes+10 { // marker only
		t.Fatalf("a multi-line plan exceeded the bound: %d runes", len([]rune(got)))
	}
	if strings.Contains(got, "3) third item") {
		t.Fatalf("the newline fallback must drop content past the cut:\n%q", clipRunes(got, 200))
	}
	// The fallback dropped everything from the cut's line onward, so the kept
	// body ends at a line boundary — no half item, no partial filler line.
	body := strings.TrimSuffix(got, "…")
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("the newline fallback left a partial line:\n%q", clipRunes(got, 200))
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("the truncation marker is lost after the newline fallback")
	}

	if got := clipPlan("short plan"); got != "short plan" {
		t.Fatalf("a plan under the bound must pass through unchanged, got %q", got)
	}
}
