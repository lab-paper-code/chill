package operations

import "testing"

func TestNilDependencyPreservesConcretePointerValidation(t *testing.T) {
	var pointer *struct{}
	if !nilDependency(nil) || !nilDependency(pointer) {
		t.Fatal("nil concrete dependencies must still be rejected through consumer contracts")
	}
	if nilDependency(&struct{}{}) || nilDependency(struct{}{}) {
		t.Fatal("present dependencies must not be rejected")
	}
}
