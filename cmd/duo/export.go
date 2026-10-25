package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/delivery"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

type exportArgs struct {
	sessionID string
	repo      string
	help      bool
}

func parseExportArgs(args []string) (exportArgs, error) {
	var out exportArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case helpRequested(arg):
			out.help = true
			return out, nil
		case arg == "--repo":
			if i+1 >= len(args) {
				return exportArgs{}, fmt.Errorf("--repo requires a path (usage: %s)", commandUsage("export"))
			}
			i++
			out.repo = strings.TrimSpace(args[i])
			if out.repo == "" {
				return exportArgs{}, fmt.Errorf("--repo requires a path (usage: %s)", commandUsage("export"))
			}
		case strings.HasPrefix(arg, "-"):
			return exportArgs{}, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("export"))
		default:
			if out.sessionID != "" {
				return exportArgs{}, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.sessionID = arg
		}
	}
	return out, nil
}

// runExport writes one session as self-contained Markdown on stdout. It reads the
// durable snapshot and the event journal, so a finished session can be archived,
// shared or reviewed offline long after its worktrees are gone.
func runExport(ctx context.Context, args []string) error {
	parsed, err := parseExportArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		printUsage()
		return nil
	}

	if parsed.sessionID == "" {
		return fmt.Errorf("no session id (usage: %s)", commandUsage("export"))
	}
	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return err
	}
	repoID, err := exportRepoID(ctx, baseDir, parsed.sessionID, parsed.repo)
	if err != nil {
		return err
	}
	store, err := sessionstore.Open(baseDir, repoID, parsed.sessionID)
	if err != nil {
		return err
	}
	snap, err := store.Load()
	if err != nil {
		return err
	}

	markdown := exportMarkdown(snap, store.OpenEvents().TUIEntries(), exportDiffstat(ctx, snap))
	fmt.Print(markdown)
	return nil
}

// exportRepoID finds the one repository that holds this session id. A session id
// is only unique within a repository, so an id that exists in several of them is
// ambiguous and asks for --repo instead of exporting whichever came first.
func exportRepoID(ctx context.Context, baseDir, sessionID, repo string) (string, error) {
	if repo != "" {
		return exportRepoRef(ctx, baseDir, repo)
	}
	repos, err := sessionstore.ListRepos(baseDir)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, repoID := range repos {
		if info, err := os.Stat(filepath.Join(baseDir, repoID, sessionID)); err == nil && info.IsDir() {
			matches = append(matches, repoID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("unknown Duo session %q; run `duo sessions --all` to list the sessions this machine has", sessionID)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("session %q exists in %d repositories (%s); choose one with --repo <path>", sessionID, len(matches), strings.Join(matches, ", "))
}

// exportRepoRef turns --repo into a repository id. A checkout resolves through the
// same discovery every other command uses; a value that names a stored repository
// id is taken as itself, because archiving a finished session is exactly when its
// checkout may no longer exist.
func exportRepoRef(ctx context.Context, baseDir, repo string) (string, error) {
	if abs, err := filepath.Abs(repo); err == nil {
		if root, err := workspace.FindRoot(ctx, abs); err == nil {
			return sessionstore.RepoID(root), nil
		}
	}
	repos, err := sessionstore.ListRepos(baseDir)
	if err != nil {
		return "", err
	}
	for _, repoID := range repos {
		if repoID == repo {
			return repoID, nil
		}
	}
	return "", fmt.Errorf("%s is neither a git repository nor a stored repository id", repo)
}

// exportDiffstat lists the files the artifact changed relative to the recorded
// base commit. Git is the ground truth, but it may be gone by the time a session
// is archived, and an export that refused to print for that reason would defeat
// its own purpose: the reason is reported in place of the list.
func exportDiffstat(ctx context.Context, snap sessionstore.Snapshot) string {
	final := snap.Delivery.FinalHead
	if final == "" {
		final = snap.Integration.Head
	}
	if final == "" || snap.Repository == "" {
		return "(no artifact head recorded, so there is no diffstat)"
	}
	changes, err := delivery.Manager{
		Repository: snap.Repository,
		BaseCommit: snap.BaseCommit,
		FinalHead:  final,
	}.ChangedPaths(ctx)
	if err != nil {
		return "(diffstat unavailable: " + err.Error() + ")"
	}
	return delivery.Summary(changes)
}

// exportMarkdown renders one session: the timeline the human saw, the plan and
// queue with their versions, both agents' signatures and evidence, the
// verification verdict, the delivery record, the diffstat and a closing summary.
// It is a pure projection of the snapshot and the journal, so it can neither
// change a session nor depend on one being live.
func exportMarkdown(snap sessionstore.Snapshot, timeline []sessionstore.TUIEntry, diffstat string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Duo session %s\n\n", orNone(snap.SessionID))
	writeExportHeader(&b, snap)
	writeExportPlan(&b, snap)
	writeExportQueue(&b, snap)
	writeExportSignatures(&b, snap)
	writeExportVerification(&b, snap)
	writeExportDelivery(&b, snap)
	b.WriteString("## Diffstat\n\n```\n" + orNone(diffstat) + "\n```\n\n")
	writeExportTimeline(&b, timeline)
	writeExportSummary(&b, snap)
	return b.String()
}

func writeExportHeader(b *strings.Builder, snap sessionstore.Snapshot) {
	base := orNone(snap.BaseBranch) + " from " + exportHash(snap.BaseCommit)
	fmt.Fprintf(b, "- project: %s (%s)\n", orNone(snap.Repository), base)
	if scope := strings.TrimSpace(snap.ScopePath); scope != "" {
		fmt.Fprintf(b, "- scope: %s\n", scope)
	}
	fmt.Fprintf(b, "- mode: %s, phase: %s\n", snap.EffectiveMode(), orNone(snap.Phase))
	if len(snap.Steps) > 0 {
		if snap.Step > len(snap.Steps) {
			fmt.Fprintf(b, "- step: all %d done\n", len(snap.Steps))
		} else {
			fmt.Fprintf(b, "- step: %d of %d\n", snap.Step, len(snap.Steps))
		}
	}
	fmt.Fprintf(b, "- created: %s, updated: %s\n", exportTime(snap.CreatedAt), exportTime(snap.UpdatedAt))
	if len(snap.AgentDrivers) > 0 {
		fmt.Fprintf(b, "- drivers: austin %s, tony %s\n", orNone(snap.AgentDrivers[protocol.Austin]), orNone(snap.AgentDrivers[protocol.Tony]))
	}
	b.WriteString("\n")
}

func writeExportPlan(b *strings.Builder, snap sessionstore.Snapshot) {
	fmt.Fprintf(b, "## Plan (v%d)\n\n%s\n\n", snap.PlanVersion, orNone(strings.TrimSpace(snap.Plan)))
}

func writeExportQueue(b *strings.Builder, snap sessionstore.Snapshot) {
	b.WriteString("## Queue\n\n")
	if len(snap.Steps) == 0 {
		b.WriteString("(no step queue: this session did not run in Step mode)\n\n")
		return
	}
	b.WriteString("```\n" + strings.TrimRight(snap.Steps.Render(), "\n") + "\n```\n\n")
}

func writeExportSignatures(b *strings.Builder, snap sessionstore.Snapshot) {
	b.WriteString("## Signatures\n\n")
	if len(snap.Ready) == 0 && len(snap.Notes) == 0 && len(snap.Evidence) == 0 {
		b.WriteString("(no signature was recorded)\n\n")
		return
	}
	b.WriteString("| agent | ready | evidence |\n| --- | --- | --- |\n")
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		fmt.Fprintf(b, "| %s | %t | %s |\n", agent, snap.Ready[agent], orNone(snap.Evidence[agent]))
	}
	// A note is the agent's own prose and can run to a paragraph. Keeping it out of
	// the table is what keeps the evidence column readable, and a note that is
	// flattened to one line would lose the structure its author wrote.
	b.WriteString("\n")
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		note := strings.TrimSpace(snap.Notes[agent])
		if note == "" {
			continue
		}
		lines := strings.Split(note, "\n")
		fmt.Fprintf(b, "- %s: %s\n", agent, lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
	b.WriteString("\n")
}

func writeExportVerification(b *strings.Builder, snap sessionstore.Snapshot) {
	b.WriteString("## Verification\n\n")
	v := snap.Verification
	if v == nil || v.Status == "" {
		b.WriteString("(no Fast-mode verdict was recorded)\n\n")
		return
	}
	fmt.Fprintf(b, "- status: %s\n- head: %s\n", v.Status, orNone(v.Head))
	if note := strings.TrimSpace(v.Note); note != "" {
		fmt.Fprintf(b, "- note: %s\n", exportCell(note))
	}
	if v.At != nil {
		fmt.Fprintf(b, "- at: %s\n", exportTime(*v.At))
	}
	b.WriteString("\n")
}

func writeExportDelivery(b *strings.Builder, snap sessionstore.Snapshot) {
	d := snap.Delivery
	b.WriteString("## Delivery\n\n")
	if d.Status == "" {
		b.WriteString("(delivery was never attempted)\n\n")
		return
	}
	fmt.Fprintf(b, "- status: %s\n", d.Status)
	if d.FinalHead != "" {
		if d.FinalBranch != "" {
			fmt.Fprintf(b, "- final head: %s on %s\n", d.FinalHead, d.FinalBranch)
		} else {
			fmt.Fprintf(b, "- final head: %s\n", d.FinalHead)
		}
	}
	if d.AppliedHead != "" {
		fmt.Fprintf(b, "- applied head: %s\n", d.AppliedHead)
	}
	if d.TargetRepo != "" {
		fmt.Fprintf(b, "- target: %s on %s\n", d.TargetRepo, orNone(d.TargetBranch))
	}
	if reason := strings.TrimSpace(d.Reason); reason != "" {
		fmt.Fprintf(b, "- reason: %s\n", exportCell(reason))
	}
	if d.AttemptedAt != nil {
		fmt.Fprintf(b, "- attempted: %s\n", exportTime(*d.AttemptedAt))
	}
	if d.CompletedAt != nil {
		fmt.Fprintf(b, "- completed: %s\n", exportTime(*d.CompletedAt))
	}
	b.WriteString("\n")
}

// writeExportTimeline renders the pane entries the human actually saw, which is
// the conversation rather than the audit journal: an export is read by a person,
// and one entry per line keeps it readable as a plain Markdown list.
func writeExportTimeline(b *strings.Builder, timeline []sessionstore.TUIEntry) {
	b.WriteString("## Timeline\n\n")
	if len(timeline) == 0 {
		b.WriteString("(the journal holds no pane entries for this session)\n\n")
		return
	}
	for _, entry := range timeline {
		speaker := entry.Pane
		if label := strings.TrimSpace(entry.Label); label != "" {
			speaker = label
		}
		text := strings.TrimSpace(entry.Text)
		if entry.Error {
			text = "error: " + text
		} else if entry.Warning {
			text = "warning: " + text
		}
		lines := strings.Split(text, "\n")
		fmt.Fprintf(b, "- %s %s: %s\n", exportTime(entry.Time), speaker, lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
	b.WriteString("\n")
}

func writeExportSummary(b *strings.Builder, snap sessionstore.Snapshot) {
	var parts []string
	parts = append(parts, fmt.Sprintf("Mode %s reached phase %s", snap.EffectiveMode(), orNone(snap.Phase)))
	if len(snap.Steps) > 0 {
		landed := 0
		for _, step := range snap.Steps {
			if step.Done {
				landed++
			}
		}
		parts = append(parts, fmt.Sprintf("%d of %d steps landed", landed, len(snap.Steps)))
	}
	switch {
	case snap.Delivery.Applied():
		parts = append(parts, "the artifact was delivered at "+exportHash(snap.Delivery.AppliedHead))
	case snap.Delivery.Status != "":
		parts = append(parts, fmt.Sprintf("delivery is %s: %s", snap.Delivery.Status, orNone(exportCell(snap.Delivery.Reason))))
	default:
		parts = append(parts, "no delivery was recorded")
	}
	b.WriteString("## Summary\n\n" + strings.Join(parts, "; ") + ".\n")
}

// exportTime is the one timestamp format the export uses. It is local time
// because the human reading the export shared the session's clock.
func exportTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// exportCell keeps one table cell on one line: a pipe or a newline in a note
// would otherwise break the Markdown table the reader is meant to skim.
func exportCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "|", "\\|"), "\n", " ")
}

// exportHash is shortHash with the "nothing recorded" case named, so a snapshot
// that never recorded a head does not read as an empty field.
func exportHash(value string) string {
	if hash := shortHash(value); hash != "" {
		return hash
	}
	return "unknown"
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}
