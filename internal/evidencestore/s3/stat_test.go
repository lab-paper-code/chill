package s3

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"testing"
)

type headClient struct {
	fakeClient
	calls int
}

func (c *headClient) HeadObject(_ context.Context, input *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	c.calls++
	return &awss3.HeadObjectOutput{ContentLength: aws.Int64(42)}, nil
}
func TestStatUsesHEADAndRejectsOtherStore(t *testing.T) {
	c := &headClient{}
	store := testObjectStore(c, t.TempDir())
	location := evidencestore.ObjectLocation{Store: store.logicalStore, Key: "input"}
	object, err := store.Stat(context.Background(), location)
	if err != nil || object.Size != 42 || c.calls != 1 {
		t.Fatalf("object=%+v calls=%d error=%v", object, c.calls, err)
	}
	location.Store = "other"
	if _, err = store.Stat(context.Background(), location); err == nil || c.calls != 1 {
		t.Fatalf("foreign store reached HEAD: %v", err)
	}
}
