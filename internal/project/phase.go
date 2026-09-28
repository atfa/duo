package project

type Phase string

const (
	PhasePlan      Phase = "PLAN"
	PhaseExecute   Phase = "EXECUTE"
	PhaseReview    Phase = "REVIEW"
	PhaseIntegrate Phase = "INTEGRATE"
	PhaseDone      Phase = "DONE"

	// Fast-mode phases. DONE is shared by both modes; the phase chain itself is
	// mode-specific and lives on Mode.Next so a goal-only chain can never be
	// advanced inside a Fast session.
	PhaseRunning Phase = "RUNNING"
	PhaseVerify  Phase = "VERIFY"
)
