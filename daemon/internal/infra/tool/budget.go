package tool

// IterationBudget bounds how many tool-call iterations an agent loop may run
// before it must stop (prevents runaway loops / cost blowups). Borrowed from
// Hermes' IterationBudget (parent ~90, subagent ~50).
type IterationBudget struct {
	remaining int
}

func NewIterationBudget(n int) *IterationBudget {
	if n < 1 {
		n = 1
	}
	return &IterationBudget{remaining: n}
}

func (b *IterationBudget) Consume() bool {
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

// Refund returns one iteration for a cheap/no-op step.
func (b *IterationBudget) Refund() { b.remaining++ }

func (b *IterationBudget) Remaining() int { return b.remaining }

const (
	ParentIterationBudget   = 90
	SubagentIterationBudget = 50
)
