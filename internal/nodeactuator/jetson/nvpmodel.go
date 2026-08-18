// Package jetson implements host adapters for NVIDIA Jetson power controls.
package jetson

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

const maximumDiagnosticOutput = 4096

// rebootRequiredMarker is how NVPModel reports that the target mode is
// established at boot rather than in place. It refuses the change and leaves
// the Node untouched, so the marker is the only signal that separates a mode
// this actuator can set from one it must restart the Node to reach.
const rebootRequiredMarker = "Reboot required for changing to this power mode"

type NVPModelOptions struct {
	Command []string
	Timeout time.Duration
}

func (o NVPModelOptions) Validate() error {
	if len(o.Command) == 0 {
		return errors.New("NVPModel command is required")
	}
	for _, argument := range o.Command {
		if strings.TrimSpace(argument) == "" || strings.TrimSpace(argument) != argument || strings.ContainsRune(argument, 0) {
			return errors.New("NVPModel command arguments must be non-empty without surrounding whitespace or NUL")
		}
	}
	if o.Timeout < time.Second || o.Timeout > 5*time.Minute {
		return errors.New("NVPModel command timeout must be between 1 second and 5 minutes")
	}
	return nil
}

type commandRunner interface {
	CombinedOutput(context.Context, []string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) CombinedOutput(ctx context.Context, command []string) ([]byte, error) {
	return exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
}

// NVPModel is the Jetson host port. Apply reports command completion only;
// the controller always calls Observe independently before accepting state.
type NVPModel struct {
	options NVPModelOptions
	runner  commandRunner
	now     func() time.Time
	restart func() error
}

func NewNVPModel(options NVPModelOptions) (*NVPModel, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &NVPModel{
		options: NVPModelOptions{
			Command: append([]string(nil), options.Command...),
			Timeout: options.Timeout,
		},
		runner:  execCommandRunner{},
		now:     time.Now,
		restart: restartNode,
	}, nil
}

func (a *NVPModel) Observe(
	ctx context.Context,
	request nodeactuator.Request,
) (nodeactuator.Observation, error) {
	if request.Target.Kind != nodeactuator.ActuatorKindJetsonNVPModel {
		return nodeactuator.Observation{}, fmt.Errorf("NVPModel cannot observe actuator kind %q", request.Target.Kind)
	}
	commandCtx, cancel, err := a.commandContext(ctx, request)
	if err != nil {
		return nodeactuator.Observation{}, err
	}
	defer cancel()
	output, commandErr := a.runner.CombinedOutput(commandCtx, appendCommand(a.options.Command, "-q"))
	if commandErr != nil {
		return nodeactuator.Observation{}, commandError("query NVPModel", commandErr, output)
	}
	mode, id, err := parseQueryOutput(output)
	if err != nil {
		return nodeactuator.Observation{}, fmt.Errorf("parse NVPModel query: %w", err)
	}
	return nodeactuator.Observation{
		PowerMode: mode,
		Target: nodeactuator.Target{
			Kind: nodeactuator.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{
				ID: id,
			},
		},
		ObservedAt: a.now().UTC(),
	}, nil
}

func (a *NVPModel) Apply(
	ctx context.Context,
	request nodeactuator.Request,
	target nodeactuator.Target,
) error {
	if request.Target.Kind != nodeactuator.ActuatorKindJetsonNVPModel ||
		target.Kind != nodeactuator.ActuatorKindJetsonNVPModel || target.JetsonNVPModel == nil ||
		target.NvidiaLockedGPUClock != nil || target.JetsonNVPModel.ID < 0 {
		return errors.New("NVPModel apply requires only a non-negative NVPModel ID")
	}
	// A Node that already holds the requested mode is left alone. This is what
	// bounds the restart below: after the Node comes back holding the target,
	// a repeated Apply reaches this return and never restarts it again.
	if current, err := a.Observe(ctx, request); err == nil && holdsTarget(current, request, target) {
		return nil
	}

	modeID := strconv.FormatInt(int64(target.JetsonNVPModel.ID), 10)
	output, commandErr := a.run(ctx, request, "-m", modeID)
	if commandErr == nil {
		return nil
	}
	if !strings.Contains(string(output), rebootRequiredMarker) {
		return commandError("apply NVPModel", commandErr, output)
	}

	// The mode is established at boot. Record it as the mode to come up in,
	// then restart the Node so the boot establishes it.
	forceOutput, forceErr := a.run(ctx, request, "--force", "-m", modeID)
	if forceErr != nil {
		return commandError("record NVPModel target", forceErr, forceOutput)
	}
	if err := a.restartNode(); err != nil {
		return fmt.Errorf("restart the Node to establish NVPModel mode %s: %w", modeID, err)
	}
	// Reached only if the restart returned without restarting anything, which
	// would leave the mode recorded and unestablished. Never report success.
	return fmt.Errorf("restart to establish NVPModel mode %s returned without restarting the Node", modeID)
}

func (a *NVPModel) run(
	ctx context.Context,
	request nodeactuator.Request,
	arguments ...string,
) ([]byte, error) {
	commandCtx, cancel, err := a.commandContext(ctx, request)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return a.runner.CombinedOutput(commandCtx, appendCommand(a.options.Command, arguments...))
}

func (a *NVPModel) restartNode() error {
	if a.restart != nil {
		return a.restart()
	}
	return restartNode()
}

func holdsTarget(
	observation nodeactuator.Observation,
	request nodeactuator.Request,
	target nodeactuator.Target,
) bool {
	return observation.PowerMode == request.PowerMode &&
		observation.Target.JetsonNVPModel != nil && target.JetsonNVPModel != nil &&
		observation.Target.JetsonNVPModel.ID == target.JetsonNVPModel.ID
}

func (a *NVPModel) commandContext(
	ctx context.Context,
	request nodeactuator.Request,
) (context.Context, context.CancelFunc, error) {
	now := a.now()
	if request.Deadline.IsZero() || !now.Before(request.Deadline) {
		return nil, nil, errors.New("NVPModel request deadline has elapsed")
	}
	deadline := now.Add(a.options.Timeout)
	if request.Deadline.Before(deadline) {
		deadline = request.Deadline
	}
	commandCtx, cancel := context.WithDeadline(ctx, deadline)
	return commandCtx, cancel, nil
}

func appendCommand(base []string, arguments ...string) []string {
	command := make([]string, 0, len(base)+len(arguments))
	command = append(command, base...)
	return append(command, arguments...)
}

func parseQueryOutput(output []byte) (string, int32, error) {
	const modePrefix = "NV Power Mode:"
	var mode string
	var id *int32
	for _, rawLine := range bytes.Split(output, []byte{'\n'}) {
		line := strings.TrimSpace(strings.TrimSuffix(string(rawLine), "\r"))
		if strings.HasPrefix(line, modePrefix) {
			if mode != "" {
				return "", 0, errors.New("query contains multiple power-mode names")
			}
			mode = strings.TrimSpace(strings.TrimPrefix(line, modePrefix))
			if mode == "" || len(strings.Fields(mode)) != 1 {
				return "", 0, errors.New("query contains an invalid power-mode name")
			}
			continue
		}
		parsed, err := strconv.ParseInt(line, 10, 32)
		if err != nil || parsed < 0 {
			continue
		}
		if id != nil {
			return "", 0, errors.New("query contains multiple power-mode IDs")
		}
		value := int32(parsed)
		id = &value
	}
	if mode == "" || id == nil {
		return "", 0, errors.New("query omitted the power-mode name or ID")
	}
	return mode, *id, nil
}

func commandError(operation string, err error, output []byte) error {
	diagnostic := strings.TrimSpace(string(output))
	if len(diagnostic) > maximumDiagnosticOutput {
		diagnostic = diagnostic[:maximumDiagnosticOutput] + " [truncated]"
	}
	if diagnostic == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, diagnostic)
}
