package s3

import (
	"context"
	"errors"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const testBucket = "chill-profiling-evidence"

func TestEnsureBucketReturnsExistingWithoutCreate(t *testing.T) {
	store := &Store{client: &fakeClient{
		headBucket: func(_ context.Context, input *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error) {
			if stringValue(input.Bucket) != testBucket {
				t.Fatalf("bucket = %q", stringValue(input.Bucket))
			}
			return &awss3.HeadBucketOutput{}, nil
		},
	}}
	outcome, err := store.EnsureBucket(context.Background(), testBucket)
	if err != nil || outcome != BucketOutcomeExisting {
		t.Fatalf("EnsureBucket() = %q, %v", outcome, err)
	}
}

func TestEnsureBucketCreatesMissingBucket(t *testing.T) {
	store := &Store{region: "us-east-1", client: &fakeClient{
		headBucket: func(context.Context, *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error) {
			return nil, bucketAPIError("NoSuchBucket")
		},
		createBucket: func(_ context.Context, input *awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error) {
			if stringValue(input.Bucket) != testBucket || input.CreateBucketConfiguration != nil {
				t.Fatalf("CreateBucket input = %#v", input)
			}
			return &awss3.CreateBucketOutput{}, nil
		},
	}}
	outcome, err := store.EnsureBucket(context.Background(), testBucket)
	if err != nil || outcome != BucketOutcomeCreated {
		t.Fatalf("EnsureBucket() = %q, %v", outcome, err)
	}
}

func TestEnsureBucketAcceptsConcurrentCreateAfterRecheck(t *testing.T) {
	headCalls := 0
	store := &Store{client: &fakeClient{
		headBucket: func(context.Context, *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error) {
			headCalls++
			if headCalls == 1 {
				return nil, bucketAPIError("NoSuchBucket")
			}
			return &awss3.HeadBucketOutput{}, nil
		},
		createBucket: func(context.Context, *awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error) {
			return nil, bucketAPIError("BucketAlreadyOwnedByYou")
		},
	}}
	outcome, err := store.EnsureBucket(context.Background(), testBucket)
	if err != nil || outcome != BucketOutcomeExisting || headCalls != 2 {
		t.Fatalf("EnsureBucket() = %q, %v, head calls %d", outcome, err, headCalls)
	}
}

func TestEnsureBucketPreservesFailureCategory(t *testing.T) {
	tests := []struct {
		name         string
		headError    error
		createError  error
		wantCategory error
	}{
		{name: "inspect authentication", headError: bucketAPIError("AccessDenied"), wantCategory: evidencestore.ErrAuthentication},
		{name: "inspect timeout", headError: context.DeadlineExceeded, wantCategory: evidencestore.ErrTimeout},
		{
			name:         "create authentication",
			headError:    bucketAPIError("NoSuchBucket"),
			createError:  bucketAPIError("AccessDenied"),
			wantCategory: evidencestore.ErrAuthentication,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &Store{client: &fakeClient{
				headBucket: func(context.Context, *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error) {
					return nil, test.headError
				},
				createBucket: func(context.Context, *awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error) {
					return nil, test.createError
				},
			}}
			_, err := store.EnsureBucket(context.Background(), testBucket)
			if !errors.Is(err, test.wantCategory) {
				t.Fatalf("EnsureBucket() error = %v, want %v", err, test.wantCategory)
			}
		})
	}
}

func TestEnsureBucketUsesConfiguredNonDefaultRegion(t *testing.T) {
	store := &Store{region: "ap-northeast-2", client: &fakeClient{
		headBucket: func(context.Context, *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error) {
			return nil, bucketAPIError("NoSuchBucket")
		},
		createBucket: func(_ context.Context, input *awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error) {
			if input.CreateBucketConfiguration == nil ||
				string(input.CreateBucketConfiguration.LocationConstraint) != "ap-northeast-2" {
				t.Fatalf("CreateBucket configuration = %#v", input.CreateBucketConfiguration)
			}
			return &awss3.CreateBucketOutput{}, nil
		},
	}}
	if _, err := store.EnsureBucket(context.Background(), testBucket); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureBucketRejectsInvalidInput(t *testing.T) {
	store := &Store{}
	for _, bucket := range []string{"", " bucket"} {
		if _, err := store.EnsureBucket(context.Background(), bucket); err == nil {
			t.Fatalf("EnsureBucket(%q) accepted", bucket)
		}
	}
}

func bucketAPIError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: "test", Fault: smithy.FaultClient}
}
