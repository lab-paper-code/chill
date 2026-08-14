package serving

import "testing"

func TestParseInferencePath(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
		want Request
	}{
		{
			name: "a model without a pinned version",
			path: "/v2/models/mobilenet/infer",
			want: Request{Model: "mobilenet"},
		},
		{
			name: "a model with a pinned version",
			path: "/v2/models/mobilenet/versions/3/infer",
			want: Request{Model: "mobilenet", Version: "3"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseInferencePath(testCase.path)
			if err != nil {
				t.Fatalf("ParseInferencePath(%q) returned %v", testCase.path, err)
			}
			if got != testCase.want {
				t.Fatalf("ParseInferencePath(%q) = %+v, want %+v", testCase.path, got, testCase.want)
			}
		})
	}
}

func TestParseInferencePathRejects(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "server readiness, which names no model", path: "/v2/health/ready"},
		{name: "model readiness, which is not an inference", path: "/v2/models/mobilenet/ready"},
		{name: "model metadata, which is not an inference", path: "/v2/models/mobilenet"},
		{name: "a path outside the protocol", path: "/infer"},
		{name: "a model segment left empty", path: "/v2/models//infer"},
		{name: "a version segment left empty", path: "/v2/models/mobilenet/versions//infer"},
		{name: "a segment past the inference verb", path: "/v2/models/mobilenet/infer/extra"},
		{name: "a misspelled versions segment", path: "/v2/models/mobilenet/version/3/infer"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got, err := ParseInferencePath(testCase.path); err == nil {
				t.Fatalf("ParseInferencePath(%q) = %+v, want an error", testCase.path, got)
			}
		})
	}
}
