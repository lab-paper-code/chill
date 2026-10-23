package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
)

func objectStore(opts options) (*stores3.Store, error) {
	return stores3.New(stores3.Config{
		LogicalStore: opts.storeName, Endpoint: opts.storeEndpoint,
		Bucket: opts.storeBucket, Region: opts.storeRegion,
		AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken: os.Getenv("AWS_SESSION_TOKEN"), TempDir: opts.observationDirectory,
	})
}

func materializeProfile(ctx context.Context, opts options) error {
	objects, err := objectStore(opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.requestTimeout)
	defer cancel()
	identity := evidencestore.ObjectIdentity{
		Store: opts.storeName, Key: opts.profileObjectKey, Size: opts.profileObjectSize,
	}
	return materializeObject(ctx, objects, identity, opts.profileOutput, opts.maxEvidenceBytes)
}

func materializeObject(ctx context.Context, objects evidencestore.Reader,
	identity evidencestore.ObjectIdentity, output string, maxBytes int64,
) (retErr error) {
	if err := identity.Validate(); err != nil {
		return err
	}
	if identity.Size > maxBytes || maxBytes <= 0 {
		return errors.New("profile object exceeds materialization bound")
	}
	stream, err := objects.Open(ctx, identity)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			retErr = errors.Join(retErr, stream.Close())
		}
	}()
	file, err := os.CreateTemp(filepath.Dir(output), ".profile-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove temporary profile: %w", err))
		}
	}()
	written, copyErr := io.Copy(file, io.LimitReader(stream, identity.Size+1))
	if copyErr != nil || written != identity.Size {
		sizeErr := fmt.Errorf("profile size mismatch: expected %d bytes, received %d", identity.Size, written)
		return errors.Join(copyErr, file.Close(), sizeErr)
	}
	if err = file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	err = stream.Close()
	closed = true
	if err != nil {
		return err
	}
	// A failed or truncated read never becomes the runner's immutable startup file.
	return os.Rename(file.Name(), output)
}
