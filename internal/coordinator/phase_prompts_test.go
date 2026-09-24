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

func TestResumePromptsArePhaseAware(t *testing.T) {
	coord := promptCoordinator()
	cases := []struct {
		phase project.Phase
		agent protocol.AgentID
		want  []string
	}{
		{project.PhasePlan, protocol.Austin, []string{"Current phase: PLAN", "Shared plan version: v7", "coordinating the shared plan", "Do not merely acknowledge"}},
		{project.PhaseExecute, protocol.Tony, []string{"Current phase: EXECUTE", "Continue your assigned EXECUTE work", "commit it", "Do not merely acknowledge"}},
		{project.PhaseReview, protocol.Tony, []string{"Current phase: REVIEW", "cross-review", "current branch/HEAD", "Do not merely acknowledge"}},
		{project.PhaseIntegrate, protocol.Austin, []string{"Current phase: INTEGRATE", "final integration validation", "current Austin integration worktree", "Do not merely acknowledge"}},
		{project.PhaseIntegrate, protocol.Tony, []string{"Current phase: INTEGRATE", "independent review of Austin's current integrated HEAD", "Do not merely acknowledge"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.phase)+string(tc.agent), func(t *testing.T) {
			coord.project.Restore(project.Snapshot{Phase: tc.phase, Plan: "authoritative plan", PlanVersion: 7})
			got := coord.ResumePrompt(tc.agent)
			for _, want := range append([]string{"[Duo session resumed]", "authoritative plan", "Austin ready:", "Tony ready:", "duo_status", "Working-directory scope:"}, tc.want...) {
				if !strings.Contains(got, want) {
					t.Fatalf("resume prompt missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestResumeIntegratePromptHonorsPartialSignatures(t *testing.T) {
	coord := promptCoordinator()
	if err := coord.project.Restore(project.Snapshot{
		Phase: project.PhaseIntegrate,
		Ready: map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: false},
	}); err != nil {
		t.Fatal(err)
	}
	austin := coord.ResumePrompt(protocol.Austin)
	tony := coord.ResumePrompt(protocol.Tony)
	for _, want := range []string{"Austin ready: true", "Tony ready: false", "current Austin integration worktree", "Do not redo the whole task"} {
		if !strings.Contains(austin, want) {
			t.Fatalf("Austin partial-sign prompt missing %q:\n%s", want, austin)
		}
	}
	for _, want := range []string{"Austin ready: true", "Tony ready: false", "independent review of Austin's current integrated HEAD", "Reject final approval"} {
		if !strings.Contains(tony, want) {
			t.Fatalf("Tony partial-sign prompt missing %q:\n%s", want, tony)
		}
	}
}
