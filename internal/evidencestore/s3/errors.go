package s3

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/smithy-go"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func translate(operation string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %s: %w", evidencestore.ErrTimeout, operation, err)
	}

	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return fmt.Errorf("%w: %s: %w", evidencestore.ErrNotFound, operation, err)
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken":
			return fmt.Errorf("%w: %s: %w", evidencestore.ErrAuthentication, operation, err)
		case "PreconditionFailed", "ConditionalRequestConflict":
			return fmt.Errorf("%w: %s: %w", evidencestore.ErrPrecondition, operation, err)
		case "BucketAlreadyExists", "BucketAlreadyOwnedByYou", "OperationAborted":
			return fmt.Errorf("%w: %s: %w", evidencestore.ErrPrecondition, operation, err)
		case "RequestTimeout", "RequestTimeoutException":
			return fmt.Errorf("%w: %s: %w", evidencestore.ErrTimeout, operation, err)
		}
	}
	return fmt.Errorf("%w: %s: %w", evidencestore.ErrTransport, operation, err)
}

func isBucketCreateRace(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	switch apiError.ErrorCode() {
	case "BucketAlreadyExists", "BucketAlreadyOwnedByYou", "OperationAborted":
		return true
	default:
		return false
	}
}

func isPrecondition(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "PreconditionFailed" || apiError.ErrorCode() == "ConditionalRequestConflict"
}
