package coordinator

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// stubWorkspace satisfies workspace.Manager for prompt tests that never touch
// Git; the embedded interface panics if an unexpected method is called.
type stubWorkspace struct {
	workspace.Manager
	set workspace.Set
}

func (s stubWorkspace) Set() workspace.Set { return s.set }

func promptCoordinator() *Coordinator {
	return New(nil, project.NewState(), harness.NewTracker(), stubWorkspace{
		set: workspace.Set{
			BaseBranch: "main",
			Austin:     workspace.Worktree{Path: "/tmp/austin", Branch: "duo/s/austin"},
			Tony:       workspace.Worktree{Path: "/tmp/tony", Branch: "duo/s/tony"},
		},
	}, nil)
}

// TestIntegratePromptRequiresFinalTreeCleanup pins the v0.4.1 hygiene duty: the
// final integrated Git tree is what the user receives, so both agents are told
// to remove collaboration-only artifacts before final sign-off.
func TestIntegratePromptRequiresFinalTreeCleanup(t *testing.T) {
	coord := promptCoordinator()
	snap := project.Snapshot{PlanVersion: 1}

	austin := coord.phasePrompt(protocol.Austin, project.PhaseReview, project.PhaseIntegrate, snap, "")
	for _, want := range []string{"temporary collaboration-only artifacts", "final Git tree", "final sign-off"} {
		if !strings.Contains(austin, want) {
			t.Fatalf("Austin INTEGRATE prompt missing %q:\n%s", want, austin)
		}
	}

	tony := coord.phasePrompt(protocol.Tony, project.PhaseReview, project.PhaseIntegrate, snap, "")
	if !strings.Contains(tony, "repository hygiene") {
		t.Fatalf("Tony INTEGRATE prompt must require repository hygiene review:\n%s", tony)
	}
	if !strings.Contains(tony, "Reject INTEGRATE") {
		t.Fatalf("Tony INTEGRATE prompt must allow rejecting a dirty final tree:\n%s", tony)
	}
}

// TestDonePromptReportsRealDelivery is the direct regression for the previous
// misleading DONE notice: DONE must state where the artifact landed, and must
// never claim the original branch was left untouched.
func TestDonePromptReportsRealDelivery(t *testing.T) {
	coord := promptCoordinator()
	coord.SetDelivery(sessionstore.Delivery{
		Status:       sessionstore.DeliveryApplied,
		FinalHead:    "abcdef1234567890",
		FinalBranch:  "duo/s/austin",
		TargetRepo:   "/tmp/repo",
		TargetBranch: "main",
		AppliedHead:  "abcdef1234567890",
	})

	prompt := coord.phasePrompt(protocol.Austin, project.PhaseIntegrate, project.PhaseDone, project.Snapshot{}, "")
	for _, want := range []string{"INTEGRATE → DONE", "Delivery complete", "original branch: main", "applied HEAD"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("DONE prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "has NOT been modified") {
		t.Fatalf("DONE prompt still carries the old no-delivery wording:\n%s", prompt)
	}
}
