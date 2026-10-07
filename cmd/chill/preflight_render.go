package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func writeBatchEnergyOptimizationPreflight(
	writer io.Writer,
	output string,
	preflight operatorconsole.BatchEnergyOptimizationPreflight,
) error {
	if output == outputFormatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(preflight)
	}
	return writeBatchEnergyOptimizationPreflightTable(writer, preflight)
}

func writeBatchEnergyOptimizationPreflightTable(
	writer io.Writer,
	preflight operatorconsole.BatchEnergyOptimizationPreflight,
) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	resolves := "no"
	if preflight.Resolves {
		resolves = "yes"
	}
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nNAMESPACE\t%s\nCAMPAIGN\t%s\nRESOLVES\t%s\n",
		preflight.ReadAt.Format(time.RFC3339Nano), display(preflight.Campaign.Namespace),
		display(preflight.Campaign.Name), resolves); err != nil {
		return err
	}
	if !preflight.Resolves {
		if _, err := fmt.Fprintf(table, "REASON\t%s\n", display(preflight.Reason)); err != nil {
			return err
		}
		return table.Flush()
	}

	var plan struct {
		Shared struct {
			PlannedBatchSizes []int `json:"plannedBatchSizes"`
			Artifact          struct {
				Name          string `json:"name"`
				ExecutionPath string `json:"executionPath"`
			} `json:"artifact"`
			DeviceClass string                                `json:"deviceClass"`
			PowerMode   string                                `json:"powerMode"`
			Tensor      profilederivation.CurveTensorContract `json:"tensor"`
		} `json:"shared"`
	}
	if len(preflight.Plan) != 0 {
		if err := json.Unmarshal(preflight.Plan, &plan); err != nil {
			return fmt.Errorf("decode BatchEnergyOptimization preflight plan: %w", err)
		}
	}
	_, err := fmt.Fprintf(table, "PLANNED_BATCH_SIZES\t%s\nARTIFACT\t%s\nEXECUTION_PATH\t%s\n"+
		"DEVICE_CLASS\t%s\nPOWER_MODE\t%s\n",
		display(formatIntegers(plan.Shared.PlannedBatchSizes)), display(plan.Shared.Artifact.Name),
		display(plan.Shared.Artifact.ExecutionPath), display(plan.Shared.DeviceClass),
		display(plan.Shared.PowerMode))
	if err != nil {
		return err
	}
	for _, tensor := range plan.Shared.Tensor.Tensors {
		if _, err := fmt.Fprintf(table, "INPUT_TENSOR\t%s\nDATATYPE\t%s\nSAMPLE_SHAPE\t%v\n",
			display(tensor.Name), display(tensor.Datatype), tensor.SampleShape); err != nil {
			return err
		}
	}
	return table.Flush()
}

func formatIntegers(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
