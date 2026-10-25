package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// The export exists so a finished round can be archived and reviewed offline, so
// the thing it must never lose is the evidence chain: which queue was approved,
// which HEAD each agent signed, what the verifier saw, and where the artifact
// landed. An export that reads well but drops those is a story, not a record.
func TestExportContainsEvidenceChain(t *testing.T) {
	at := time.Date(2026, 10, 25, 10, 40, 53, 0, time.UTC)
	snap := sessionstore.Snapshot{
		SessionID:   "20261025-103543-ee3eeb3f",
		Repository:  "/repo/duo",
		BaseBranch:  "main",
		BaseCommit:  "e36bdebd8d1234567890",
		Phase:       "DONE",
		Plan:        "Implement T24, then T11, then T19.",
		PlanVersion: 4,
		Mode:        project.ModeStep,
		Step:        2,
		Steps: project.Steps{
			{Title: "T24 remote pause", Done: true, Policy: project.ModeGoal, DeliveryHead: "landedsha123456"},
			{Title: "T11 export", Policy: project.ModeFast, Head: "submittedsha7890"},
		},
		Ready:    map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		Notes:    map[protocol.AgentID]string{protocol.Austin: "the Go side is committed"},
		Evidence: map[protocol.AgentID]string{protocol.Austin: "austin-evidence-sha", protocol.Tony: "tony-evidence-sha"},
		Verification: &sessionstore.Verification{
			Status: "passed",
			Head:   "verified-head-sha",
			Note:   "make check green",
			At:     &at,
		},
		Delivery: sessionstore.Delivery{
			Status:       sessionstore.DeliveryApplied,
			FinalHead:    "final-head-sha",
			FinalBranch:  "duo/session/austin",
			TargetRepo:   "/repo/duo",
			TargetBranch: "main",
			AppliedHead:  "applied-head-sha",
		},
		CreatedAt: at,
		UpdatedAt: at,
	}
	timeline := []sessionstore.TUIEntry{
		{Time: at, Pane: "austin", Text: "first line\nsecond line"},
		{Time: at, Pane: "tony", Label: "Austin → Tony", Text: "peer note", Warning: true},
	}

	out := exportMarkdown(snap, timeline, "M\tinternal/remote/client.go")

	for _, want := range []string{
		// the queue and the plan version it was approved with
		"## Plan (v4)",
		"Implement T24, then T11, then T19.",
		"## Queue",
		"1. [x] [goal] T24 remote pause",
		"2. [ ] [fast] T11 export",
		// both agents' signatures and the exact evidence each one signed
		"| Austin | true | austin-evidence-sha |",
		"| Tony | true | tony-evidence-sha |",
		"- Austin: the Go side is committed",
		"tony-evidence-sha",
		// the verifier's verdict, bound to the HEAD it saw
		"- status: passed",
		"- head: verified-head-sha",
		// where the artifact landed
		"- status: applied",
		"- applied head: applied-head-sha",
		// the diffstat and the timeline the human saw
		"M\tinternal/remote/client.go",
		"austin: first line",
		"  second line",
		"warning: peer note",
		// the closing summary counts what landed
		"1 of 2 steps landed",
		"the artifact was delivered at applied-he",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the export does not contain %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "<html") || strings.Contains(out, "<div") {
		t.Error("the export contains HTML; it is Markdown only")
	}
}

// A session id is only unique within a repository, so an id that exists in two of
// them must ask which one instead of exporting whichever was found first. A
// checkout that no longer exists is still exportable by its stored repository id,
// which is exactly the case an archive has to survive.
func TestExportResolvesTheRepositoryOrAsksForIt(t *testing.T) {
	baseDir := t.TempDir()
	for _, repoID := range []string{"repo-a", "repo-b"} {
		if err := os.MkdirAll(filepath.Join(baseDir, repoID, "shared-session"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "repo-b", "only-here"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := exportRepoID(ctx, baseDir, "only-here", ""); err != nil {
		t.Fatalf("a unique session id was not resolved: %v", err)
	}
	_, err := exportRepoID(ctx, baseDir, "shared-session", "")
	if err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("an ambiguous session id resolved without asking: %v", err)
	}
	if !strings.Contains(err.Error(), "repo-a") || !strings.Contains(err.Error(), "repo-b") {
		t.Errorf("the ambiguity error names no candidate: %v", err)
	}
	if _, err := exportRepoID(ctx, baseDir, "shared-session", "repo-b"); err != nil {
		t.Errorf("--repo did not select a stored repository id: %v", err)
	}
	if _, err := exportRepoID(ctx, baseDir, "missing", ""); err == nil || !strings.Contains(err.Error(), "unknown Duo session") {
		t.Errorf("an unknown session id = %v, want an error naming it", err)
	}
}
