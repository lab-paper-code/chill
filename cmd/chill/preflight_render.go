package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lab-paper-code/chill/internal/queryapi"
)

func writeBatchEnergyOptimizationPreflight(
	writer io.Writer,
	output string,
	preflight queryapi.BatchEnergyOptimizationPreflight,
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
	preflight queryapi.BatchEnergyOptimizationPreflight,
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

	if preflight.Session == nil {
		return table.Flush()
	}
	_, err := fmt.Fprintf(table, "PLANNED_BATCH_SIZES\t%s\nMINIMUM_SAMPLES\t%d\nSLO_PERCENTILE\t%g\n",
		display(formatIntegers(preflight.Session.PlannedBatchSizes)),
		preflight.Session.MinimumSamples, preflight.Session.SLOPercentile)
	if err != nil {
		return err
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
