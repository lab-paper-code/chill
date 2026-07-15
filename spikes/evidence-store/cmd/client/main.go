package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
)

const (
	verificationSchemaVersion    = "spikes.chill.dacs.io/evidence-verification.v1alpha1"
	bucketBootstrapSchemaVersion = "spikes.chill.dacs.io/evidence-bucket-bootstrap.v1alpha1"
)

type evidenceStore interface {
	evidencestore.ImmutableWriter
	evidencestore.Reader
	EnsureBucket(context.Context, string) (evidences3.BucketOutcome, error)
}

type environment func(string) (string, bool)
type storeFactory func(evidences3.Config) (evidenceStore, error)

type verification struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Object        evidencestore.ObjectIdentity `json:"object"`
	Result        string                       `json:"result"`
}

type bucketBootstrap struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Bucket        string                   `json:"bucket"`
	Outcome       evidences3.BucketOutcome `json:"outcome"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.LookupEnv, newStore, os.Stdout, os.Stderr))
}

func run(
	ctx context.Context,
	args []string,
	lookup environment,
	newStore storeFactory,
	stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		writeDiagnostic(stderr, "usage: evidence-store-client <ensure-bucket|put|verify|expect-conflict> [flags]\n")
		return 2
	}
	switch args[0] {
	case "ensure-bucket", "put", "verify", "expect-conflict":
	default:
		writeDiagnostic(stderr, "evidence-store-client: unsupported command %q\n", args[0])
		return 2
	}

	config, bucket, err := loadStoreEnvironment(lookup)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client: configuration: %v\n", err)
		return 2
	}
	store, err := newStore(config)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client: create store: %v\n", err)
		return 2
	}
	if args[0] == "ensure-bucket" {
		return runEnsureBucket(ctx, args[1:], store, bucket, stdout, stderr)
	}
	identity, err := loadObjectEnvironment(lookup, bucket)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client: object configuration: %v\n", err)
		return 2
	}
	switch args[0] {
	case "put":
		return runPut(ctx, args[1:], store, identity, stdout, stderr)
	case "verify":
		return runVerify(ctx, args[1:], store, identity, stdout, stderr)
	case "expect-conflict":
		return runExpectConflict(ctx, args[1:], store, identity, stdout, stderr)
	}
	return 2
}

func runEnsureBucket(
	ctx context.Context,
	args []string,
	store evidenceStore,
	bucket string,
	stdout, stderr io.Writer,
) int {
	flags := flag.NewFlagSet("ensure-bucket", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		writeDiagnostic(stderr, "evidence-store-client ensure-bucket: positional arguments are not accepted\n")
		return 2
	}

	outcome, err := store.EnsureBucket(ctx, bucket)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client ensure-bucket: %v\n", err)
		return 1
	}
	result := bucketBootstrap{
		SchemaVersion: bucketBootstrapSchemaVersion,
		Bucket:        bucket,
		Outcome:       outcome,
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		writeDiagnostic(stderr, "evidence-store-client ensure-bucket: write result: %v\n", err)
		return 1
	}
	return 0
}

func runExpectConflict(
	ctx context.Context,
	args []string,
	store evidenceStore,
	existing evidencestore.ObjectIdentity,
	stdout, stderr io.Writer,
) int {
	flags := flag.NewFlagSet("expect-conflict", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("file", "", "path to evidence bytes that must conflict")
	digest := flags.String("digest", "", "canonical SHA-256 digest of the conflicting bytes")
	size := flags.Int64("size", 0, "byte size of the conflicting bytes")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*input) == "" {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: --file, --digest, and --size are required\n")
		return 2
	}
	candidate := existing
	candidate.Digest = *digest
	candidate.Size = *size
	if err := candidate.Validate(); err != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: candidate identity: %v\n", err)
		return 2
	}

	content, err := os.Open(*input)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: open candidate: %v\n", err)
		return 1
	}
	_, putErr := store.PutImmutable(ctx, candidate, content)
	closeErr := content.Close()
	if closeErr != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: close candidate: %v\n", closeErr)
		return 1
	}
	if !errors.Is(putErr, evidencestore.ErrPrecondition) {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: wanted immutable conflict, got %v\n", putErr)
		return 1
	}

	retained, err := store.Open(ctx, existing)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: open retained evidence: %v\n", err)
		return 1
	}
	verifyErr := evidencestore.Verify(retained, existing)
	closeErr = retained.Close()
	if verifyErr != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: verify retained evidence: %v\n", verifyErr)
		return 1
	}
	if closeErr != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: close retained evidence: %v\n", closeErr)
		return 1
	}

	result := verification{
		SchemaVersion: verificationSchemaVersion,
		Object:        existing,
		Result:        "RejectedDifferentBytes",
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		writeDiagnostic(stderr, "evidence-store-client expect-conflict: write result: %v\n", err)
		return 1
	}
	return 0
}

func runPut(
	ctx context.Context,
	args []string,
	store evidencestore.ImmutableWriter,
	identity evidencestore.ObjectIdentity,
	stdout, stderr io.Writer,
) int {
	flags := flag.NewFlagSet("put", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("file", "", "path to the complete evidence bundle")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*input) == "" {
		writeDiagnostic(stderr, "evidence-store-client put: --file is required and positional arguments are not accepted\n")
		return 2
	}

	content, err := os.Open(*input)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client put: open evidence: %v\n", err)
		return 1
	}
	defer func() { _ = content.Close() }()

	receipt, err := store.PutImmutable(ctx, identity, content)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client put: %v\n", err)
		return 1
	}
	if err := receipt.Validate(); err != nil {
		writeDiagnostic(stderr, "evidence-store-client put: invalid receipt: %v\n", err)
		return 1
	}
	if receipt.Object != identity {
		writeDiagnostic(stderr, "evidence-store-client put: receipt object does not match the requested identity\n")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		writeDiagnostic(stderr, "evidence-store-client put: write receipt: %v\n", err)
		return 1
	}
	return 0
}

func runVerify(
	ctx context.Context,
	args []string,
	store evidencestore.Reader,
	identity evidencestore.ObjectIdentity,
	stdout, stderr io.Writer,
) int {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		writeDiagnostic(stderr, "evidence-store-client verify: positional arguments are not accepted\n")
		return 2
	}

	content, err := store.Open(ctx, identity)
	if err != nil {
		writeDiagnostic(stderr, "evidence-store-client verify: open evidence: %v\n", err)
		return 1
	}
	verifyErr := evidencestore.Verify(content, identity)
	closeErr := content.Close()
	if verifyErr != nil {
		writeDiagnostic(stderr, "evidence-store-client verify: %v\n", verifyErr)
		return 1
	}
	if closeErr != nil {
		writeDiagnostic(stderr, "evidence-store-client verify: close evidence: %v\n", closeErr)
		return 1
	}

	result := verification{
		SchemaVersion: verificationSchemaVersion,
		Object:        identity,
		Result:        "ExactMatch",
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		writeDiagnostic(stderr, "evidence-store-client verify: write result: %v\n", err)
		return 1
	}
	return 0
}

func loadStoreEnvironment(lookup environment) (evidences3.Config, string, error) {
	required := func(name string) (string, error) {
		value, present := lookup(name)
		if !present || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}

	endpoint, err := required("STORE_ENDPOINT")
	if err != nil {
		return evidences3.Config{}, "", err
	}
	region, err := required("AWS_DEFAULT_REGION")
	if err != nil {
		return evidences3.Config{}, "", err
	}
	accessKey, err := required("AWS_ACCESS_KEY_ID")
	if err != nil {
		return evidences3.Config{}, "", err
	}
	secretKey, err := required("AWS_SECRET_ACCESS_KEY")
	if err != nil {
		return evidences3.Config{}, "", err
	}
	storeName, err := required("STORE_BUCKET")
	if err != nil {
		return evidences3.Config{}, "", err
	}
	config := evidences3.Config{
		Endpoint:        endpoint,
		Region:          region,
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	}
	if sessionToken, present := lookup("AWS_SESSION_TOKEN"); present {
		config.SessionToken = sessionToken
	}
	return config, storeName, nil
}

func loadObjectEnvironment(lookup environment, storeName string) (evidencestore.ObjectIdentity, error) {
	required := func(name string) (string, error) {
		value, present := lookup(name)
		if !present || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}

	key, err := required("STORE_KEY")
	if err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	digest, err := required("EXPECTED_DIGEST")
	if err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	sizeText, err := required("EXPECTED_SIZE")
	if err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return evidencestore.ObjectIdentity{}, fmt.Errorf(
			"EXPECTED_SIZE must be a base-10 integer: %w",
			err,
		)
	}

	identity := evidencestore.ObjectIdentity{Store: storeName, Key: key, Digest: digest, Size: size}
	if err := identity.Validate(); err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	return identity, nil
}

func newStore(config evidences3.Config) (evidenceStore, error) {
	return evidences3.New(config)
}

func writeDiagnostic(writer io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(writer, format, args...)
}
