package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSessionResolutionClassification(t *testing.T) {
	resource := schema.GroupResource{Group: "edge.chill.io", Resource: "modelspecs"}
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"timeout", apierrors.NewTimeoutError("API timeout", 1), true},
		{"server timeout", apierrors.NewServerTimeout(resource, "get", 1), true},
		{"rate limit", apierrors.NewTooManyRequests("API throttled", 1), true},
		{"unavailable", apierrors.NewServiceUnavailable("API unavailable"), true},
		{"internal", apierrors.NewInternalError(errors.New("API internal error")), true},
		{"deadline", context.DeadlineExceeded, true},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"connection reset", syscall.ECONNRESET, true},
		{"plain incompatible", errors.New("artifact architecture does not match DeviceClass"), false},
		{"missing reference", apierrors.NewNotFound(resource, "missing"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := fmt.Errorf("resolve static candidate: %w", test.err)
			classified := sessionResolutionError(original)
			var retry *servingkube.SessionResolutionRetry
			if errors.As(classified, &retry) != test.retry || classified.Error() != original.Error() || !errors.Is(classified, test.err) {
				t.Fatalf("classified %T %v retry %v", classified, classified, test.retry)
			}
		})
	}
}
