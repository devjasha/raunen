package agent

// Mode decides what the agent is allowed to do without asking.
type Mode int

const (
	// ModeAuto runs every tool immediately. This is the default.
	ModeAuto Mode = iota
	// ModeAccept runs read-only tools freely but asks before anything that
	// changes state.
	ModeAccept
	// ModePlan refuses anything that changes state, so the model can inspect
	// and propose without touching the working tree.
	ModePlan
)

// Modes is the cycle order, which is what Tab steps through.
var Modes = []Mode{ModeAuto, ModeAccept, ModePlan}

func (m Mode) String() string {
	switch m {
	case ModeAccept:
		return "accept edits"
	case ModePlan:
		return "plan"
	default:
		return "auto"
	}
}

// Next returns the following mode in the cycle.
func (m Mode) Next() Mode {
	for i, x := range Modes {
		if x == m {
			return Modes[(i+1)%len(Modes)]
		}
	}
	return ModeAuto
}

// guidance is appended to the system prompt so the model knows the rules it is
// working under. Without it a model in plan mode keeps proposing edits and
// running into refusals.
func (m Mode) guidance() string {
	switch m {
	case ModeAccept:
		return "\n\nEvery change you make is shown to the user for approval before it " +
			"runs. Make one change at a time and say what it does first."
	case ModePlan:
		return "\n\nYou are in plan mode. You cannot write files, edit files, or run " +
			"commands that change anything — those tools will refuse. Investigate " +
			"with the read-only tools, then reply with a concrete plan for the user " +
			"to approve. Do not claim to have made changes."
	default:
		return ""
	}
}
