package v2

import "github.com/lab-paper-code/chill/internal/scheduler/protocol"

// state retains the adapter's numerical scratch and exposes its current selection.
type state struct {
	Table  *protocol.Table
	Choice []int
	port   protocol.State
}

func newState(p protocol.Problem, t *protocol.Table, choice []int) *state {
	port := p.NewState(choice)
	return &state{Table: t, Choice: port.Choice(), port: port}
}
func (s *state) reset(choice []int)           { s.port.Reset(choice) }
func (s *state) apply(n, q int)               { s.port.Apply(n, q) }
func (s *state) delta(n, q int) protocol.Rank { return s.port.Delta(n, q) }
