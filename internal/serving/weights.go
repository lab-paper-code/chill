package serving

import (
	"errors"
	"fmt"
)

// Backend is one serving node and the routing weight the scheduler assigned to
// it. Each serving node is reached through its own Service, so the entry point
// keeps the choice and leaves the transport to the cluster network.
type Backend struct {
	Node    string
	Service string
	Weight  int32
}

// Weights is the routing weight vector for one model: w in
// docs/spec/03_routing.md, restricted to the nodes serving that model. The
// specification states w as fractions summing to 1; here they are non-negative
// integers whose sum is the denominator.
type Weights []Backend

// Validate reports whether the vector can distribute a request. A model no node
// serves is an empty vector rather than an all-zero one.
func (w Weights) Validate() error {
	if len(w) == 0 {
		return errors.New("no node serves this model")
	}
	nodes := make(map[string]struct{}, len(w))
	for _, backend := range w {
		if backend.Node == "" {
			return errors.New("a backend names no node")
		}
		if _, duplicate := nodes[backend.Node]; duplicate {
			return fmt.Errorf("node %q appears twice", backend.Node)
		}
		nodes[backend.Node] = struct{}{}
		if backend.Service == "" {
			return fmt.Errorf("node %q names no Service", backend.Node)
		}
		if backend.Weight < 0 {
			return fmt.Errorf("node %q carries a negative weight", backend.Node)
		}
	}
	if w.total() == 0 {
		return errors.New("no node carries a positive weight")
	}
	return nil
}

// Choose returns the backend that serves one request. Pick is a draw from any
// uniform source, reduced modulo the total weight; the caller owns the entropy,
// so the choice stays a function of its inputs and a test can name the outcome
// exactly.
//
// A node weighted zero is never chosen, which is how a drain takes a node out of
// service while it remains in the vector.
func (w Weights) Choose(pick uint64) (Backend, error) {
	if err := w.Validate(); err != nil {
		return Backend{}, fmt.Errorf("validate weights: %w", err)
	}
	offset := int64(pick % uint64(w.total()))
	for _, backend := range w {
		offset -= int64(backend.Weight)
		if offset < 0 {
			return backend, nil
		}
	}
	return Backend{}, errors.New("the draw fell past every backend")
}

func (w Weights) total() int64 {
	var total int64
	for _, backend := range w {
		total += int64(backend.Weight)
	}
	return total
}
