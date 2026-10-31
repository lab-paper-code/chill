// Command evaluation-evidence streams immutable evaluation artifacts using the existing store.
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
	"syscall"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/evidencestore/s3"
)

func main() {
	operation := flag.String("operation", "put", "put or get")
	key := flag.String("key", "", "object key")
	file := flag.String("file", "", "local file")
	size := flag.Int64("size", 0, "expected read size; zero discovers the location")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *operation, *key, *file, *size, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, evidencestore.ErrNotFound) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, operation, key, name string, size int64, output io.Writer) (resultErr error) {
	const staging = "/work/.evidence-tmp"
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	store, err := s3.New(s3.Config{
		LogicalStore: os.Getenv("CHILL_EVIDENCE_STORE"), Bucket: os.Getenv("CHILL_EVIDENCE_BUCKET"),
		Endpoint: os.Getenv("CHILL_EVIDENCE_ENDPOINT"), Region: os.Getenv("CHILL_EVIDENCE_REGION"),
		AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken: os.Getenv("AWS_SESSION_TOKEN"), TempDir: staging,
	})
	if err != nil {
		return err
	}
	identity := evidencestore.ObjectIdentity{Store: os.Getenv("CHILL_EVIDENCE_STORE"), Key: key, Size: size}
	switch operation {
	case "put":
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
		stat, err := file.Stat()
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return errors.New("artifact must be regular")
		}
		identity.Size = stat.Size()
		receipt, err := store.PutImmutable(ctx, identity, file)
		if err != nil {
			return err
		}
		if receipt.Outcome != evidencestore.CreateOutcomeCreated {
			return fmt.Errorf("%w: evaluation artifact already exists", evidencestore.ErrPrecondition)
		}
	case "get":
		var reader io.ReadCloser
		if size > 0 {
			reader, err = store.Open(ctx, identity)
		} else {
			reader, err = store.OpenLocation(ctx, evidencestore.ObjectLocation{Store: identity.Store, Key: key})
		}
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
		file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		copied, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		if size > 0 && size != copied {
			return evidencestore.ErrContentMismatch
		}
		identity.Size = copied
	default:
		return errors.New("operation must be put or get")
	}
	return json.NewEncoder(output).Encode(identity)
}
