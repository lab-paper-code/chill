package firstfloor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"time"
)

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func durationOK(ms float64) bool {
	return finite(ms) && ms >= 0 && ms < float64(math.MaxInt64)/float64(time.Millisecond)
}
func decodeStrict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("exactly one JSON object required")
	}
	return nil
}
func readBounded(path string, cap int) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(cap)+1))
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if len(b) > cap {
		return nil, errors.New("file exceeds configured byte cap")
	}
	return b, e
}
func ReadConfig(path string) (*Config, error) {
	data, err := readBounded(path, MaxHeaderBytes)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err = decodeStrict(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Mode == modeServing {
		raw, e := readBounded(cfg.ProfilePath, MaxFrameBytes)
		if e != nil {
			return nil, e
		}
		var bundle ProfileBundle
		if e = decodeStrict(raw, &bundle); e != nil {
			return nil, e
		}
		if bundle.ProfileID == "" || bundle.ProfileID != cfg.Policy.ProfileID {
			return nil, errors.New("immutable profile ID differs from policy")
		}
		cfg.Policy.Batches = bundle.Batches
	}
	if err = ValidateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
func ValidateConfig(c *Config) error {
	if c.Model == "" || len(c.Model) > 256 || len(c.RunnerCommand) == 0 || c.RunnerCommand[0] == "" {
		return errors.New("model and runner command required")
	}
	if !uniqueNames(c.Inputs) || !uniqueNames(c.Outputs) || c.MaxNewTokens < 0 {
		return errors.New("nonempty unique bounded input and output field names and nonnegative maxNewTokens required")
	}
	if c.Mode != modeServing && c.Mode != modeProfiling {
		return errors.New("mode must be serving or profiling")
	}
	c.Policy.DeadlinePolicy = EffectiveDeadlinePolicy(c.Policy.DeadlinePolicy)
	switch c.Policy.DeadlinePolicy {
	case DeadlineDrop, DeadlineKeep, DeadlineDefer:
	default:
		return errors.New("deadlinePolicy must be drop, keep or defer")
	}
	if c.RunnerTimeoutMS <= 0 || !durationOK(float64(c.RunnerTimeoutMS)) || c.MaxQueueRequests < 1 || c.MaxQueueRequests > 1<<20 || c.MaxQueueBytes < 1 || c.MaxQueueBytes > MaxFrameBytes || c.MaxRequestBytes < 1 || c.MaxRequestBytes > MaxFrameBytes {
		return errors.New("invalid time or body/queue capacity")
	}
	if err := validateInitializedBatches(c); err != nil {
		return err
	}
	if c.Mode == modeServing {
		return validatePolicy(c.Policy, c.BatchSizes)
	}
	if c.Policy.FixedBatchSize != 0 {
		return errors.New("fixedBatchSize applies to serving only")
	}
	return nil
}
func uniqueNames(names []string) bool {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "" || len(n) > 256 || seen[n] {
			return false
		}
		seen[n] = true
	}
	return len(names) > 0
}
func validateInitializedBatches(c *Config) error {
	sort.Ints(c.BatchSizes)
	if len(c.BatchSizes) == 0 || c.Mode == modeServing && c.BatchSizes[0] != 1 || c.MaxBatchSize < 1 || c.MaxBatchSize > c.MaxQueueRequests {
		return errors.New("nonempty initialized batches and bounded maxBatchSize required; serving needs batch-1")
	}
	prev := 0
	for _, k := range c.BatchSizes {
		if k <= prev || k > c.MaxBatchSize {
			return errors.New("batch exceeds initialized capacity")
		}
		prev = k
	}
	return nil
}
func validatePolicy(p Policy, sizes []int) error {
	if !identity(p.DecisionID) || !identity(p.ProfileID) || !durationOK(p.MaxBatchWaitMS) {
		return errors.New("bounded decision/profile IDs required")
	}
	sort.Slice(p.Batches, func(i, j int) bool { return p.Batches[i].Size < p.Batches[j].Size })
	if len(p.Batches) == 0 || p.Batches[0].Size != 1 {
		return errors.New("measured batch-1 required")
	}
	prev := 0
	for _, b := range p.Batches {
		i := sort.SearchInts(sizes, b.Size)
		if b.Size <= prev || i == len(sizes) || sizes[i] != b.Size || len(b.ServiceSamplesMS) == 0 || !finite(b.EnergyPerItemJ) || b.EnergyPerItemJ < 0 {
			return fmt.Errorf("invalid measured batch %d", b.Size)
		}
		for _, s := range b.ServiceSamplesMS {
			if s <= 0 || !durationOK(s) {
				return errors.New("positive finite raw service samples required")
			}
		}
		sort.Float64s(b.ServiceSamplesMS)
		prev = b.Size
	}
	return validateBatchCap(p, sizes)
}
func validateBatchCap(p Policy, sizes []int) error {
	if p.MaxBatchSize < 0 || p.FixedBatchSize < 0 {
		return errors.New("policy maxBatchSize and fixedBatchSize must be nonnegative")
	}
	if p.MaxBatchSize != 0 && (!initialized(sizes, p.MaxBatchSize) || !measured(p, p.MaxBatchSize)) {
		return errors.New("policy batch cap was not initialized and measured")
	}
	// Initialized sizes are already within maxBatchSize and the queue capacity.
	if p.FixedBatchSize != 0 && (!allowedBatch(p, p.FixedBatchSize) || !initialized(sizes, p.FixedBatchSize) || !measured(p, p.FixedBatchSize)) {
		return errors.New("fixed batch size must be an allowed, initialized and measured batch")
	}
	return nil
}
func measured(p Policy, size int) bool {
	for _, b := range p.Batches {
		if b.Size == size {
			return true
		}
	}
	return false
}

// WatchConfig retains the initialized execution and immutable samples. Only the
// small policy changes; queued request contexts and running snapshots are untouched.
func WatchConfig(ctx context.Context, path string, s *Scheduler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previous []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Done():
			return
		case <-ticker.C:
		}
		data, e := readBounded(path, MaxHeaderBytes)
		if e != nil || bytes.Equal(data, previous) {
			continue
		}
		previous = data
		var cfg Config
		e = decodeStrict(data, &cfg)
		if e == nil {
			sort.Ints(cfg.BatchSizes)
			e = s.Update(ctx, &cfg)
		}
		if e != nil {
			slog.Warn("configuration rejected; retaining applied snapshot", "error", e)
		}
	}
}
