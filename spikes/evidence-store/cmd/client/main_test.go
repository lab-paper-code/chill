package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
)

func TestRunPut(t *testing.T) {
	content := []byte("immutable profiling evidence")
	input := writeFixture(t, content)
	identity := testIdentity(content)
	store := &fakeStore{put: func(got evidencestore.ObjectIdentity, reader io.Reader) (evidencestore.Receipt, error) {
		if got != identity {
			t.Fatalf("PutImmutable identity = %+v, want %+v", got, identity)
		}
		actual, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read candidate: %v", err)
		}
		if !bytes.Equal(actual, content) {
			t.Fatalf("candidate = %q, want %q", actual, content)
		}
		return evidencestore.Receipt{
			SchemaVersion: evidencestore.ReceiptSchemaVersion,
			Object:        identity,
			Outcome:       evidencestore.CreateOutcomeCreated,
		}, nil
	}}
	var stdout, stderr bytes.Buffer

	exitCode := run(
		context.Background(), []string{"put", "--file", input},
		testEnvironment(identity), fixedStore(store), &stdout, &stderr,
	)
	if exitCode != 0 {
		t.Fatalf("run() exitCode = %d, stderr = %q", exitCode, stderr.String())
	}
	var receipt evidencestore.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Object != identity || receipt.Outcome != evidencestore.CreateOutcomeCreated {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestRunEnsureBucketNeedsNoObjectIdentity(t *testing.T) {
	identity := testIdentity([]byte("unused"))
	lookup := testEnvironment(identity)
	store := &fakeStore{ensureBucket: func(bucket string) (evidences3.BucketOutcome, error) {
		if bucket != identity.Store {
			t.Fatalf("EnsureBucket bucket = %q", bucket)
		}
		return evidences3.BucketOutcomeCreated, nil
	}}
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"ensure-bucket"}, func(name string) (string, bool) {
		if name == "STORE_KEY" || name == "EXPECTED_DIGEST" || name == "EXPECTED_SIZE" {
			return "", false
		}
		return lookup(name)
	}, fixedStore(store), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exitCode = %d, stderr = %q", exitCode, stderr.String())
	}
	var result bucketBootstrap
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode bucket bootstrap: %v", err)
	}
	if result.Bucket != identity.Store || result.Outcome != evidences3.BucketOutcomeCreated {
		t.Fatalf("bucket bootstrap = %+v", result)
	}
}

func TestRunVerify(t *testing.T) {
	content := []byte("immutable profiling evidence")
	identity := testIdentity(content)
	store := &fakeStore{open: func(got evidencestore.ObjectIdentity) (io.ReadCloser, error) {
		if got != identity {
			t.Fatalf("Open identity = %+v, want %+v", got, identity)
		}
		return io.NopCloser(bytes.NewReader(content)), nil
	}}
	var stdout, stderr bytes.Buffer

	exitCode := run(
		context.Background(), []string{"verify"},
		testEnvironment(identity), fixedStore(store), &stdout, &stderr,
	)
	if exitCode != 0 {
		t.Fatalf("run() exitCode = %d, stderr = %q", exitCode, stderr.String())
	}
	var result verification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode verification: %v", err)
	}
	if result.Object != identity || result.Result != "ExactMatch" {
		t.Fatalf("verification = %+v", result)
	}
}

func TestRunRejectsMissingConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"verify"}, func(string) (string, bool) {
		return "", false
	}, fixedStore(&fakeStore{}), &stdout, &stderr)
	if exitCode != 2 || !bytes.Contains(stderr.Bytes(), []byte("STORE_ENDPOINT is required")) {
		t.Fatalf("run() exitCode = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunVerifyRejectsMismatch(t *testing.T) {
	content := []byte("immutable profiling evidence")
	identity := testIdentity(content)
	store := &fakeStore{open: func(evidencestore.ObjectIdentity) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(append([]byte{'X'}, content[1:]...))), nil
	}}
	var stdout, stderr bytes.Buffer

	exitCode := run(
		context.Background(), []string{"verify"},
		testEnvironment(identity), fixedStore(store), &stdout, &stderr,
	)
	if exitCode != 1 || !bytes.Contains(stderr.Bytes(), []byte(evidencestore.ErrContentMismatch.Error())) {
		t.Fatalf("run() exitCode = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunExpectConflict(t *testing.T) {
	existingContent := []byte("immutable profiling evidence")
	candidateContent := []byte("different profiling evidence")
	existing := testIdentity(existingContent)
	candidate := testIdentity(candidateContent)
	candidate.Store = existing.Store
	candidate.Key = existing.Key
	candidatePath := writeFixture(t, candidateContent)
	store := &fakeStore{
		put: func(got evidencestore.ObjectIdentity, reader io.Reader) (evidencestore.Receipt, error) {
			if got != candidate {
				t.Fatalf("PutImmutable identity = %+v, want %+v", got, candidate)
			}
			return evidencestore.Receipt{}, evidencestore.ErrPrecondition
		},
		open: func(got evidencestore.ObjectIdentity) (io.ReadCloser, error) {
			if got != existing {
				t.Fatalf("Open identity = %+v, want %+v", got, existing)
			}
			return io.NopCloser(bytes.NewReader(existingContent)), nil
		},
	}
	args := []string{
		"expect-conflict",
		"--file", candidatePath,
		"--digest", candidate.Digest,
		"--size", fmt.Sprintf("%d", candidate.Size),
	}
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), args, testEnvironment(existing), fixedStore(store), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() exitCode = %d, stderr = %q", exitCode, stderr.String())
	}
	var result verification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode verification: %v", err)
	}
	if result.Object != existing || result.Result != "RejectedDifferentBytes" {
		t.Fatalf("verification = %+v", result)
	}
}

func writeFixture(t *testing.T, content []byte) string {
	t.Helper()
	path := t.TempDir() + "/evidence.json"
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func testIdentity(content []byte) evidencestore.ObjectIdentity {
	sum := sha256.Sum256(content)
	return evidencestore.ObjectIdentity{
		Store:  "chill-profiling-evidence",
		Key:    "runs/run-1.json",
		Digest: fmt.Sprintf("sha256:%x", sum),
		Size:   int64(len(content)),
	}
}

func testEnvironment(identity evidencestore.ObjectIdentity) environment {
	values := map[string]string{
		"STORE_ENDPOINT":        "http://chill-evidence-store-spike.chill-system.svc.cluster.local:9000",
		"STORE_BUCKET":          identity.Store,
		"STORE_KEY":             identity.Key,
		"EXPECTED_DIGEST":       identity.Digest,
		"EXPECTED_SIZE":         fmt.Sprintf("%d", identity.Size),
		"AWS_ACCESS_KEY_ID":     "access-key",
		"AWS_SECRET_ACCESS_KEY": "secret-key",
		"AWS_DEFAULT_REGION":    "us-east-1",
	}
	return func(name string) (string, bool) {
		value, present := values[name]
		return value, present
	}
}

func fixedStore(store evidenceStore) storeFactory {
	return func(evidences3.Config) (evidenceStore, error) {
		return store, nil
	}
}

type fakeStore struct {
	put          func(evidencestore.ObjectIdentity, io.Reader) (evidencestore.Receipt, error)
	open         func(evidencestore.ObjectIdentity) (io.ReadCloser, error)
	ensureBucket func(string) (evidences3.BucketOutcome, error)
}

func (s *fakeStore) EnsureBucket(_ context.Context, bucket string) (evidences3.BucketOutcome, error) {
	if s.ensureBucket == nil {
		return "", errors.New("unexpected EnsureBucket call")
	}
	return s.ensureBucket(bucket)
}

func (s *fakeStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	if s.put == nil {
		return evidencestore.Receipt{}, errors.New("unexpected PutImmutable call")
	}
	return s.put(identity, content)
}

func (s *fakeStore) Open(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	if s.open == nil {
		return nil, errors.New("unexpected Open call")
	}
	return s.open(identity)
}
