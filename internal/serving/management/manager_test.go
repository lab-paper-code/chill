package management

import "testing"

func TestConsumerDependencyPreservesTypedNilRejection(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "nil interface", want: true},
		{name: "typed nil pointer", value: (*Manager)(nil), want: true},
		{name: "present pointer", value: &Manager{}},
		{name: "value implementation", value: struct{}{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nilDependency(test.value); got != test.want {
				t.Fatalf("nil dependency = %v, want %v", got, test.want)
			}
		})
	}
}
