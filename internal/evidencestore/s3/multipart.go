package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// multipartPartSize is S3's minimum non-final part size. One sequential buffer
// bounds payload memory at 5 MiB; the 10,000-part protocol limit caps an object
// at approximately 48.8 GiB. No compressed object is staged on disk.
const multipartPartSize = 5 << 20

var _ evidencestore.StreamingImmutableWriter = (*Store)(nil)

func (s *Store) StreamImmutable(ctx context.Context, location evidencestore.ObjectLocation, produce func(io.Writer) error) (result evidencestore.Receipt, err error) {
	if err := location.Validate(); err != nil {
		return result, err
	}
	if err := s.validateLocation(location); err != nil {
		return result, err
	}
	if produce == nil {
		return result, fmt.Errorf("%w: producer is nil", evidencestore.ErrContentMismatch)
	}
	callCtx, cancel := s.multipartRequestContext(ctx)
	created, err := s.client.CreateMultipartUpload(callCtx, &awss3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(location.Key)})
	cancel()
	if err != nil {
		return result, translate("start evidence multipart upload", err)
	}
	if created == nil || aws.ToString(created.UploadId) == "" {
		return result, fmt.Errorf("%w: multipart upload omitted ID", evidencestore.ErrTransport)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Cancellation must not prevent releasing server-side multipart parts.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, abortErr := s.client.AbortMultipartUpload(cleanup, &awss3.AbortMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(location.Key), UploadId: created.UploadId})
		if abortErr != nil {
			err = errors.Join(err, translate("abort evidence multipart upload", abortErr))
		}
	}()
	writer := &multipartWriter{ctx: ctx, store: s, key: location.Key, uploadID: created.UploadId, buffer: make([]byte, multipartPartSize)}
	if err = produce(writer); err != nil {
		return result, err
	}
	if writer.err != nil {
		return result, writer.err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if writer.used > 0 || len(writer.parts) == 0 {
		if err = writer.flush(); err != nil {
			return result, err
		}
	}
	callCtx, cancel = s.multipartRequestContext(ctx)
	_, err = s.client.CompleteMultipartUpload(callCtx, &awss3.CompleteMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(location.Key), UploadId: created.UploadId, IfNoneMatch: aws.String("*"), MultipartUpload: &types.CompletedMultipartUpload{Parts: writer.parts}})
	cancel()
	outcome := evidencestore.CreateOutcomeCreated
	if err != nil {
		var apiError smithy.APIError
		if !errors.As(err, &apiError) || apiError.ErrorCode() != preconditionFailedCode {
			return result, translate("complete evidence multipart upload", err)
		}
		outcome = evidencestore.CreateOutcomeExisting
		err = nil
	} else {
		committed = true
	}
	return receipt(evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: writer.size}, outcome), nil
}

func (s *Store) multipartRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.requestTimeout > 0 {
		return context.WithTimeout(ctx, s.requestTimeout)
	}
	return context.WithCancel(ctx)
}

type multipartWriter struct {
	ctx      context.Context
	store    *Store
	key      string
	uploadID *string
	buffer   []byte
	used     int
	size     int64
	parts    []types.CompletedPart
	err      error
}

func (w *multipartWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		if w.err != nil {
			return written, w.err
		}
		if w.err = w.ctx.Err(); w.err != nil {
			return written, w.err
		}
		n := copy(w.buffer[w.used:], data)
		w.used += n
		w.size += int64(n)
		written += n
		data = data[n:]
		if w.used == len(w.buffer) {
			w.err = w.flush()
			if w.err != nil {
				return written, w.err
			}
		}
	}
	return written, w.err
}

func (w *multipartWriter) flush() error {
	if len(w.parts) >= 10000 {
		return fmt.Errorf("%w: multipart object exceeds 10000 parts", evidencestore.ErrContentMismatch)
	}
	number := int32(len(w.parts) + 1)
	callCtx, cancel := w.store.multipartRequestContext(w.ctx)
	output, err := w.store.client.UploadPart(callCtx, &awss3.UploadPartInput{Bucket: aws.String(w.store.bucket), Key: aws.String(w.key), UploadId: w.uploadID, PartNumber: aws.Int32(number), ContentLength: aws.Int64(int64(w.used)), Body: bytes.NewReader(w.buffer[:w.used])})
	cancel()
	if err != nil {
		return translate("upload evidence part", err)
	}
	if output == nil || aws.ToString(output.ETag) == "" {
		return fmt.Errorf("%w: upload part omitted ETag", evidencestore.ErrTransport)
	}
	w.parts = append(w.parts, types.CompletedPart{ETag: output.ETag, PartNumber: aws.Int32(number)})
	w.used = 0
	return nil
}
