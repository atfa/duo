package project

type Phase string

const (
	PhasePlan      Phase = "PLAN"
	PhaseExecute   Phase = "EXECUTE"
	PhaseReview    Phase = "REVIEW"
	PhaseIntegrate Phase = "INTEGRATE"
	PhaseDone      Phase = "DONE"
)

func (p Phase) Next() (Phase, bool) {
	switch p {
	case PhasePlan:
		return PhaseExecute, true
	case PhaseExecute:
		return PhaseReview, true
	case PhaseReview:
		return PhaseIntegrate, true
	case PhaseIntegrate:
		return PhaseDone, true
	default:
		return p, false
	}
}
