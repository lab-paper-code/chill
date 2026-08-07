package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

var (
	_ evidencestore.ImmutableWriter = (*Store)(nil)
	_ evidencestore.Reader          = (*Store)(nil)
	_ evidencestore.LocationReader  = (*Store)(nil)
)

type Store struct {
	client       client
	region       string
	logicalStore string
	bucket       string
	tempDir      string
}

func (s *Store) PutImmutable(
	ctx context.Context,
	expected evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	if err := s.validateLocation(expected.Location()); err != nil {
		return evidencestore.Receipt{}, err
	}
	candidate, err := s.prepareCandidate(expected, content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	defer removeCandidate(candidate)

	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(expected.Key),
		Body:          candidate,
		ContentLength: aws.Int64(expected.Size),
		IfNoneMatch:   aws.String("*"),
	})
	if err == nil {
		return receipt(expected, evidencestore.CreateOutcomeCreated), nil
	}
	if !isPrecondition(err) {
		return evidencestore.Receipt{}, translate("create evidence object", err)
	}
	return receipt(expected, evidencestore.CreateOutcomeExisting), nil
}

func (s *Store) Open(
	ctx context.Context,
	expected evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	return s.OpenLocation(ctx, expected.Location())
}

func (s *Store) OpenLocation(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	if err := location.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateLocation(location); err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(location.Key),
	})
	if err != nil {
		return nil, translate("open evidence object", err)
	}
	if output == nil || output.Body == nil {
		return nil, fmt.Errorf("%w: S3 returned an empty response body", evidencestore.ErrTransport)
	}
	return &translatedReadCloser{body: output.Body}, nil
}

func (s *Store) validateLocation(location evidencestore.ObjectLocation) error {
	if location.Store != s.logicalStore {
		return fmt.Errorf(
			"%w: logical store %q does not match configured store %q",
			evidencestore.ErrInvalidIdentity,
			location.Store,
			s.logicalStore,
		)
	}
	return nil
}

func (s *Store) prepareCandidate(
	expected evidencestore.ObjectIdentity,
	content io.Reader,
) (*os.File, error) {
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	if content == nil {
		return nil, fmt.Errorf("%w: content reader is nil", evidencestore.ErrContentMismatch)
	}

	candidate, err := os.CreateTemp(s.tempDir, "chill-evidence-*")
	if err != nil {
		return nil, fmt.Errorf("create bounded evidence candidate: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			removeCandidate(candidate)
		}
	}()

	written, err := io.CopyN(candidate, content, expected.Size)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: expected %d bytes, read %d", evidencestore.ErrContentMismatch, expected.Size, written)
		}
		return nil, fmt.Errorf("stage evidence candidate: %w", err)
	}
	var extra [1]byte
	if _, err := io.ReadFull(content, extra[:]); err == nil {
		return nil, fmt.Errorf("%w: content exceeds expected size %d", evidencestore.ErrContentMismatch, expected.Size)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read evidence candidate boundary: %w", err)
	}
	if _, err := candidate.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind evidence candidate: %w", err)
	}
	failed = false
	return candidate, nil
}

func receipt(identity evidencestore.ObjectIdentity, outcome evidencestore.CreateOutcome) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity,
		Outcome:       outcome,
	}
}

func removeCandidate(candidate *os.File) {
	name := candidate.Name()
	_ = candidate.Close()
	_ = os.Remove(name)
}

type translatedReadCloser struct {
	body io.ReadCloser
}

func (r *translatedReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.body.Read(buffer)
	if err == nil || errors.Is(err, io.EOF) {
		return count, err
	}
	return count, translate("read evidence object", err)
}

func (r *translatedReadCloser) Close() error {
	if err := r.body.Close(); err != nil {
		return translate("close evidence object", err)
	}
	return nil
}
