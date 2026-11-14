package observation

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// ScanSessionRows visits stored request rows whose workload UID starts with
// workloadPrefix, from receipt hours intersecting [from, to]. Receipt hours are
// deliberately coarse: native timestamps belong to the edge clock and never
// select session evidence. Nested row data is read-only. This read neither
// widens nor populates the rolling writer cache.
func (l *ObservationLog) ScanSessionRows(ctx context.Context, workloadPrefix string, from, to time.Time, yield func(serving.RequestRow) error) error {
	read, err := l.prepare(ctx, map[string][2]time.Time{observationRequestStream: {from, to}})
	if err != nil {
		return err
	}
	defer read.close()
	return l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var row serving.RequestRow
		if err := json.Unmarshal(raw, &row); err != nil {
			slog.Warn("unreadable session observation row", "reason", err)
			return nil
		}
		if strings.HasPrefix(row.WorkloadUID, workloadPrefix) {
			return yield(row)
		}
		return nil
	})
}
