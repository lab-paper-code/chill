package s3

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func TestTranslate(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "canceled", err: context.Canceled, wantErr: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, wantErr: evidencestore.ErrTimeout},
		{name: "not found", err: apiError("NoSuchKey"), wantErr: evidencestore.ErrNotFound},
		{name: "authentication", err: apiError("AccessDenied"), wantErr: evidencestore.ErrAuthentication},
		{name: "precondition", err: apiError("PreconditionFailed"), wantErr: evidencestore.ErrPrecondition},
		{name: "bucket conflict", err: apiError("BucketAlreadyExists"), wantErr: evidencestore.ErrPrecondition},
		{name: "API timeout", err: apiError("RequestTimeout"), wantErr: evidencestore.ErrTimeout},
		{name: "transport", err: errors.New("connection reset"), wantErr: evidencestore.ErrTransport},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := translate("test operation", test.err)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("translate() error = %v, want %v", err, test.wantErr)
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("translate() error = %v, want original error preserved", err)
			}
		})
	}
}

func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: "test", Fault: smithy.FaultClient}
}
