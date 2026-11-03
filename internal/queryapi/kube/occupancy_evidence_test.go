package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
)

type occupancyEvidenceReader struct {
	payload                    string
	openErr, readErr, closeErr error
	locations                  []evidencestore.ObjectLocation
	closed                     bool
	readBytes                  int
}

func (r *occupancyEvidenceReader) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	r.locations = append(r.locations, location)
	if r.openErr != nil {
		return nil, r.openErr
	}
	return &occupancyEvidenceStream{owner: r, reader: strings.NewReader(r.payload)}, nil
}

type occupancyEvidenceStream struct {
	owner  *occupancyEvidenceReader
	reader *strings.Reader
}

func (s *occupancyEvidenceStream) Read(p []byte) (int, error) {
	if s.owner.readErr != nil {
		return 0, s.owner.readErr
	}
	n, err := s.reader.Read(p)
	s.owner.readBytes += n
	return n, err
}
func (s *occupancyEvidenceStream) Close() error { s.owner.closed = true; return s.owner.closeErr }

func TestReadMeasurementEvidencePreservesBoundAndCloseErrors(t *testing.T) {
	readFailure, closeFailure, openFailure := errors.New("read"), errors.New("close"), errors.New("open")
	location := evidencestore.ObjectLocation{Store: "evidence", Key: "retained.json"}
	for _, tc := range []struct {
		name   string
		reader occupancyEvidenceReader
		want   string
		err    error
		closed bool
	}{
		{name: "exact bound", reader: occupancyEvidenceReader{payload: "1234"}, want: "1234", closed: true},
		{name: "oversize", reader: occupancyEvidenceReader{payload: "123456789"}, err: errors.New("measurement evidence exceeds read bound"), closed: true},
		{name: "open", reader: occupancyEvidenceReader{openErr: openFailure}, err: openFailure},
		{name: "read", reader: occupancyEvidenceReader{readErr: readFailure}, err: readFailure, closed: true},
		{name: "close", reader: occupancyEvidenceReader{payload: "1234", closeErr: closeFailure}, err: closeFailure, closed: true},
		{name: "read and close", reader: occupancyEvidenceReader{readErr: readFailure, closeErr: closeFailure}, err: errors.Join(readFailure, closeFailure), closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadMeasurementEvidence(context.Background(), &tc.reader, 4, location)
			if string(got) != tc.want || (err == nil) != (tc.err == nil) ||
				(err != nil && err.Error() != tc.err.Error()) {
				t.Fatalf("read result %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
			if tc.reader.closed != tc.closed || tc.reader.readBytes > 5 || len(tc.reader.locations) != 1 ||
				tc.reader.locations[0] != location {
				t.Fatalf("bound, close or location changed: %+v", tc.reader)
			}
			if tc.name == "read and close" && (!errors.Is(err, readFailure) || !errors.Is(err, closeFailure)) {
				t.Fatal("joined error lost a cause")
			}
		})
	}
}

func TestProjectDispatchOccupancyReadFailureDistinctions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader occupancyEvidenceReader
		code   string
	}{
		{"missing", occupancyEvidenceReader{openErr: evidencestore.ErrNotFound}, "OccupancyEvidenceMissing"},
		{"unavailable", occupancyEvidenceReader{openErr: errors.New("denied")}, "OccupancyEvidenceUnavailable"},
		{"oversize", occupancyEvidenceReader{payload: "123456"}, "OccupancyEvidenceUnavailable"},
		{"corrupt", occupancyEvidenceReader{payload: "no"}, "OccupancyEvidenceInvalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := api.DispatchOccupancyMeasurement{}
			cr.Status.Result = &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "retained.json"}
			decodeCalls := 0
			decoder := func(_ api.DispatchOccupancyMeasurement, raw []byte) (queryapi.DecodedOccupancyEvidence, error) {
				decodeCalls++
				if string(raw) != tc.reader.payload {
					t.Fatalf("decoder received changed bytes: %q", raw)
				}
				return queryapi.DecodedOccupancyEvidence{}, errors.New("invalid source record")
			}
			got, err := ProjectDispatchOccupancy(context.Background(), &tc.reader, 4, cr, decoder)
			wantDecodeCalls := 0
			if tc.name == "corrupt" {
				wantDecodeCalls = 1
			}
			if decodeCalls != wantDecodeCalls {
				t.Fatalf("decode count %d; want %d", decodeCalls, wantDecodeCalls)
			}
			if err != nil || !strings.Contains(string(got.Assessment), tc.code) || got.Record != nil ||
				got.Burst != nil {
				t.Fatalf("failure classification changed: %+v %v", got, err)
			}
		})
	}
	reader := &occupancyEvidenceReader{openErr: errors.New("must not open")}
	if _, err := ProjectDispatchOccupancy(context.Background(), reader, 4, api.DispatchOccupancyMeasurement{}, func(api.DispatchOccupancyMeasurement, []byte) (queryapi.DecodedOccupancyEvidence, error) {
		t.Fatal("missing reference caused decode")
		return queryapi.DecodedOccupancyEvidence{}, nil
	}); err != nil ||
		len(reader.locations) != 0 {
		t.Fatalf("missing reference caused IO: %v %+v", err, reader.locations)
	}
}

// decodeOccupancyEvidenceFixture reads query-owned fields in valid test fixtures.
// Production source-wire validation is supplied by the composition root decoder.
func decodeOccupancyEvidenceFixture(
	cr api.DispatchOccupancyMeasurement,
	raw []byte,
) (queryapi.DecodedOccupancyEvidence, error) {
	var header struct {
		SchemaVersion   string `json:"schemaVersion"`
		PerRequestNanos *int64 `json:"perRequestNanos"`
	}
	headerErr := json.Unmarshal(raw, &header)
	out := queryapi.DecodedOccupancyEvidence{HasPerRequestNanos: header.PerRequestNanos != nil}
	out.Burst = headerErr == nil && (header.SchemaVersion == queryapi.BurstOccupancySchema ||
		strings.HasPrefix(cr.Status.Result.Key, queryapi.BurstOccupancyPrefix))
	var record struct {
		queryapi.OccupancyEvidenceRecord
		Estimate *queryapi.OccupancyEstimate `json:"estimate"`
	}
	err := json.Unmarshal(raw, &record)
	out.Record = record.OccupancyEvidenceRecord
	if out.Burst {
		out.Record.Estimate = record.Estimate
	}
	return out, err
}
