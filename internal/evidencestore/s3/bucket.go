package s3

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type BucketOutcome string

const (
	BucketOutcomeCreated  BucketOutcome = "Created"
	BucketOutcomeExisting BucketOutcome = "Existing"
)

// EnsureBucket creates the configured bucket only when it is not already
// accessible with the configured credentials.
func (s *Store) EnsureBucket(ctx context.Context) (BucketOutcome, error) {
	if strings.TrimSpace(s.bucket) == "" {
		return "", errors.New("bucket is required")
	}
	if strings.TrimSpace(s.bucket) != s.bucket {
		return "", errors.New("bucket must not have surrounding whitespace")
	}

	if err := s.headBucket(ctx); err == nil {
		return BucketOutcomeExisting, nil
	} else if !errors.Is(err, evidencestore.ErrNotFound) {
		return "", err
	}

	input := &awss3.CreateBucketInput{Bucket: aws.String(s.bucket)}
	if s.region != "" && s.region != "us-east-1" {
		input.CreateBucketConfiguration = &awss3types.CreateBucketConfiguration{
			LocationConstraint: awss3types.BucketLocationConstraint(s.region),
		}
	}
	if _, err := s.client.CreateBucket(ctx, input); err == nil {
		return BucketOutcomeCreated, nil
	} else if !isBucketCreateRace(err) {
		return "", translate("create evidence bucket", err)
	} else if headErr := s.headBucket(ctx); headErr == nil {
		return BucketOutcomeExisting, nil
	} else if !errors.Is(headErr, evidencestore.ErrNotFound) {
		return "", fmt.Errorf("verify concurrently created evidence bucket: %w", headErr)
	} else {
		return "", translate("create evidence bucket", err)
	}
}

func (s *Store) headBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return translate("inspect evidence bucket", err)
	}
	return nil
}
