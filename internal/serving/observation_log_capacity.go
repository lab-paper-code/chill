package serving

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// ObservationArchive reads the hourly archive objects the log uploads.
type ObservationArchive interface {
	evidencestore.Reader
	evidencestore.Lister
}

var capacityRowBuiltMarker = []byte(`"kind":"` + CapacityRowBuiltEvent + `"`)

// CapacityRecords returns the latest capacity-row-built row recorded for each
// of keys: local management segments first, then archived hours newest first
// until every key is found when the archive writer also reads.
// ponytail: a key never recorded scans the whole archive; bound the hours if it grows large.
func (l *ObservationLog) CapacityRecords(ctx context.Context, keys []string) (map[string]CapacityRow, error) {
	found := map[string]CapacityRow{}
	visit := func(into map[string]CapacityRow) func([]byte) error {
		return func(raw []byte) error {
			if !bytes.Contains(raw, capacityRowBuiltMarker) {
				return nil
			}
			var e ManagementEvent
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			if e.Capacity != nil && slices.Contains(keys, e.Capacity.Key) {
				into[e.Capacity.Key] = *e.Capacity // chronological: the later row wins
			}
			return nil
		}
	}
	read, err := l.capture(map[string][2]time.Time{observationManagementStream: {}})
	if err != nil {
		return nil, err
	}
	read.ctx = ctx
	err = l.scanStream(read, observationManagementStream, visit(found))
	read.close()
	archive, reads := l.config.Writer.(ObservationArchive)
	if err != nil || !reads || len(found) == len(keys) {
		return found, err
	}
	objects, err := archive.List(ctx, observationArchivePrefix)
	if err != nil {
		return found, err
	}
	slices.SortFunc(objects, func(a, b evidencestore.ObjectIdentity) int { return strings.Compare(b.Key, a.Key) })
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return found, err
		}
		if hour, _, valid := observationObjectHour(object); !valid || read.localHours[hour] {
			continue
		}
		if !strings.HasSuffix(object.Key, "/"+observationManagementStream+".jsonl.gz") {
			continue
		}
		hour := map[string]CapacityRow{}
		if err := readArchivedStream(ctx, archive, object, visit(hour)); err != nil {
			return found, err
		}
		for key, row := range hour {
			if _, newer := found[key]; !newer {
				found[key] = row
			}
		}
		if len(found) == len(keys) {
			break
		}
	}
	return found, nil
}

func readArchivedStream(ctx context.Context, archive evidencestore.Reader, object evidencestore.ObjectIdentity, visit func([]byte) error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := archive.Open(ctx, object)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, body.Close()) }()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	unzipped, err := gzip.NewReader(observationContextReader{ctx: ctx, source: body})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unzipped.Close()) }()
	scanner := observationScanner{requireNewline: true}
	err = scanner.scan(unzipped, -1, func(raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return visit(raw)
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return observationScanError(err)
}
