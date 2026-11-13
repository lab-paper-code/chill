package firstfloor

import (
	"slices"
	"strconv"
)

// BuildRunnerCommand projects the frozen execution recipe. Mode, fixed batch
// dispatch and profile delivery do not change runner initialization or warmup.
// Warmup covers every initialized batch size, so no first execution at any
// of them is a cold start.
func (c *Config) BuildRunnerCommand() {
	c.WarmupBatchSizes = slices.Clone(c.BatchSizes)
	c.RunnerCommand = []string{
		"python3", "/opt/chill/runtime/firstfloor/runner.py", "--runtime", c.Backend,
		"--model-dir", "/model", "--preprocessor", "/preprocessor",
		"--warmup-iterations", strconv.Itoa(c.WarmupIterations),
	}
	for _, batch := range c.WarmupBatchSizes {
		c.RunnerCommand = append(c.RunnerCommand, "--warmup-batch", strconv.Itoa(batch))
	}
	if c.GPUUUID != "" {
		c.RunnerCommand = append(c.RunnerCommand, "--gpu-uuid", c.GPUUUID)
	}
	if c.Backend == "rknnlite" {
		c.RunnerCommand = append(c.RunnerCommand, "--delivery-json", "/config/delivery.json")
	}
}
