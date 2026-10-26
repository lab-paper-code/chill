// Package nvidia implements discrete NVIDIA GPU clock actuation.
package nvidia

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

// Options pins installation-owned host tooling and the declared exact clocks.
type Options struct {
	Command []string
	Timeout time.Duration
	Clocks  []int32
}

func (o Options) Validate() error {
	if len(o.Command) == 0 || o.Timeout < time.Second || o.Timeout > 5*time.Minute || len(o.Clocks) == 0 {
		return errors.New("NVIDIA command, timeout (1s–5m), and declared clocks are required")
	}
	for _, token := range o.Command {
		if token == "" || strings.TrimSpace(token) != token || strings.ContainsRune(token, 0) {
			return errors.New("NVIDIA command tokens must be non-empty without surrounding whitespace or NUL")
		}
	}
	for _, mhz := range o.Clocks {
		if mhz < 1 {
			return errors.New("NVIDIA declared clock must be positive")
		}
	}
	return nil
}

type Clock struct {
	options Options
	run     func(context.Context, []string) ([]byte, error)
	now     func() time.Time
}

func New(options Options) (*Clock, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	options.Command = append([]string(nil), options.Command...)
	options.Clocks = append([]int32(nil), options.Clocks...)
	return &Clock{options: options, now: time.Now, run: func(ctx context.Context, argv []string) ([]byte, error) {
		return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	}}, nil
}

// Observe reports only independently observed current graphics/SM MHz. NVSMI
// 555 does not expose a locked-range getter; never synthesize that from intent.
func (a *Clock) Observe(ctx context.Context, request nodeactuator.Request) (nodeactuator.Observation, error) {
	if request.Target.Kind != nodeactuator.ActuatorKindNvidiaLockedGPUClock {
		return nodeactuator.Observation{}, errors.New("NVIDIA observation requires NvidiaLockedGPUClock")
	}
	// Deliberately no -i here: reject multiple GPUs rather than silently managing
	// one device while publishing Node-wide state.
	output, err := a.command(ctx, request, "--query-gpu=clocks.current.graphics,clocks.current.sm", "--format=csv,noheader,nounits")
	if err != nil {
		return nodeactuator.Observation{}, err
	}
	rows := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(rows) != 1 {
		return nodeactuator.Observation{}, errors.New("NVIDIA observation requires exactly one GPU")
	}
	fields := strings.Split(rows[0], ",")
	if len(fields) != 2 {
		return nodeactuator.Observation{}, errors.New("NVIDIA observation requires graphics and SM clocks")
	}
	graphics, graphicsErr := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 32)
	sm, smErr := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 32)
	if graphicsErr != nil || smErr != nil || graphics < 1 || graphics != sm {
		return nodeactuator.Observation{}, fmt.Errorf("unestablished NVIDIA graphics/SM clocks: %q", strings.TrimSpace(string(output)))
	}
	return nodeactuator.Observation{Target: nodeactuator.Target{
		Kind:                 nodeactuator.ActuatorKindNvidiaLockedGPUClock,
		NvidiaLockedGPUClock: &nodeactuator.NvidiaLockedGPUClockTarget{MinMHz: int32(graphics), MaxMHz: int32(graphics)},
	}, ObservedAt: a.now().UTC()}, nil
}

// Apply verifies the operating point and rolls back failures. The controller
// still performs its own independent Observe before publishing convergence.
func (a *Clock) Apply(ctx context.Context, request nodeactuator.Request, target nodeactuator.Target) error {
	if err := target.Validate(); err != nil {
		return err
	}
	clock := target.NvidiaLockedGPUClock
	if request.Target.Kind != nodeactuator.ActuatorKindNvidiaLockedGPUClock || clock == nil ||
		clock.MinMHz != clock.MaxMHz || clock.Mode != nil || !a.declared(clock.MinMHz) {
		return errors.New("NVIDIA apply requires a declared exact clock with mode omitted (default algorithm)")
	}
	previous, err := a.Observe(ctx, request)
	if err != nil {
		return fmt.Errorf("observe before NVIDIA apply: %w", err)
	}
	_, applyErr := a.set(ctx, request, clock.MinMHz)
	if applyErr == nil {
		observed, readErr := a.Observe(ctx, request)
		if readErr == nil && observed.Target.NvidiaLockedGPUClock.MinMHz == clock.MinMHz {
			return nil
		}
		applyErr = errors.Join(errors.New("NVIDIA target read-back mismatch"), readErr)
	}
	// Cleanup has its own bounded deadline even if the apply timed out. It is
	// safety restoration, not another attempt at applying the failed target.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.options.Timeout)
	defer cancel()
	restore := request
	restore.Deadline = a.now().Add(a.options.Timeout)
	prior := previous.Target.NvidiaLockedGPUClock.MinMHz
	var restoreErr error
	if a.declared(prior) {
		_, restoreErr = a.set(cleanup, restore, prior)
	} else {
		_, restoreErr = a.command(cleanup, restore, "-i", "0", "-rgc")
	}
	observed, readErr := a.Observe(cleanup, restore)
	if readErr == nil && a.declared(prior) && observed.Target.NvidiaLockedGPUClock.MinMHz != prior {
		readErr = errors.New("NVIDIA previous clock read-back mismatch")
	}
	if restoreErr != nil || readErr != nil {
		return errors.Join(applyErr, fmt.Errorf("NVIDIA restoration failed: %w", errors.Join(restoreErr, readErr)))
	}
	if !a.declared(prior) {
		return errors.Join(applyErr, errors.New("NVIDIA driver defaults requested; unlocked policy is not queryable"))
	}
	return fmt.Errorf("NVIDIA apply failed; previous clock restored: %w", applyErr)
}

func (a *Clock) declared(mhz int32) bool {
	for _, candidate := range a.options.Clocks {
		if candidate == mhz {
			return true
		}
	}
	return false
}

func (a *Clock) set(ctx context.Context, request nodeactuator.Request, mhz int32) ([]byte, error) {
	value := strconv.FormatInt(int64(mhz), 10)
	return a.command(ctx, request, "-i", "0", "-lgc", value+","+value)
}

func (a *Clock) command(ctx context.Context, request nodeactuator.Request, args ...string) ([]byte, error) {
	now := a.now()
	if request.Deadline.IsZero() || !now.Before(request.Deadline) {
		return nil, errors.New("NVIDIA request deadline has elapsed")
	}
	deadline := now.Add(a.options.Timeout)
	if request.Deadline.Before(deadline) {
		deadline = request.Deadline
	}
	commandCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	argv := make([]string, 0, len(a.options.Command)+len(args))
	argv = append(argv, a.options.Command...)
	output, err := a.run(commandCtx, append(argv, args...))
	if err != nil {
		diagnostic := output
		if len(diagnostic) > 4096 {
			diagnostic = diagnostic[:4096]
		}
		return output, fmt.Errorf("nvidia-smi: %w: %s", err, strings.TrimSpace(string(diagnostic)))
	}
	return output, nil
}
