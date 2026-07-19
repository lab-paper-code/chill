package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func writeEvidencePassport(writer io.Writer, output string, passport operatorconsole.EvidencePassport) error {
	if output == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(passport)
	}
	return writeEvidencePassportTable(writer, passport)
}

func writeEvidencePassportTable(writer io.Writer, passport operatorconsole.EvidencePassport) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	run := passport.Run
	if err := writePassportSection(table, "RUN", [][2]string{
		{"Name", run.Namespace + "/" + run.Name},
		{"UID", display(run.UID)},
		{"Generation", fmt.Sprint(run.Generation)},
		{"Read at", passport.ReadAt.Format(time.RFC3339Nano)},
		{"Model", run.Spec.Candidate.ModelSpec},
		{"Execution path", run.Spec.Candidate.ExecutionPath},
		{"Device class", run.Spec.Candidate.DeviceClass},
		{"Target node", run.Spec.TargetNode},
		{"Power mode", run.Spec.Candidate.PowerMode},
	}); err != nil {
		return err
	}

	runtime := passport.Measurement.Runtime
	if err := writePassportSection(table, "RUNTIME MEASUREMENT", [][2]string{
		{"Runtime", runtime.Family + " " + runtime.Version},
		{"Provider", runtime.Provider},
		{"Architecture", runtime.Architecture},
		{"Batch", fmt.Sprint(runtime.Batch)},
		{"Warm-up iterations", fmt.Sprint(runtime.WarmupIterations)},
		{"Window", runtime.MeasurementStarted.Format(time.RFC3339Nano) + " -> " + runtime.MeasurementEnded.Format(time.RFC3339Nano)},
		{"Elapsed", formatSeconds(runtime.ElapsedSeconds)},
		{"Completed calls", fmt.Sprint(runtime.CompletedCalls)},
		{"Completed items", fmt.Sprint(runtime.CompletedItems)},
		{"Throughput", fmt.Sprintf("%.3f items/s", runtime.ThroughputItems)},
		{"Output validation", fmt.Sprintf("%s (passed=%t)", runtime.OutputValidation.Method, runtime.OutputValidation.Passed)},
	}); err != nil {
		return err
	}

	latency := runtime.Latency
	if err := writePassportSection(table, "LATENCY", [][2]string{
		{"Samples", fmt.Sprint(latency.Count)},
		{"Mean", formatMilliseconds(latency.Mean)},
		{"P50", formatMilliseconds(latency.P50)},
		{"P95", formatMilliseconds(latency.P95)},
		{"P99", formatMilliseconds(latency.P99)},
	}); err != nil {
		return err
	}

	power := passport.Measurement.Power
	wattMinimum, wattMean, wattMaximum := "unknown", "unknown", "unknown"
	if power.Watts != nil {
		wattMinimum = formatWatts(power.Watts.Minimum)
		wattMean = formatWatts(power.Watts.Mean)
		wattMaximum = formatWatts(power.Watts.Maximum)
	}
	if err := writePassportSection(table, "POWER OBSERVATION", [][2]string{
		{"Source", power.Source.Kind + " / " + power.Source.NodeName + " / " + power.Source.Metric},
		{"Window", power.StartedAt.Format(time.RFC3339Nano) + " -> " + power.EndedAt.Format(time.RFC3339Nano)},
		{"Duration", formatSeconds(power.ObservationSeconds)},
		{"Successful samples", fmt.Sprint(power.Samples)},
		{"Failed reads", fmt.Sprint(power.Failures)},
		{"Watts minimum", wattMinimum},
		{"Watts mean", wattMean + " (arithmetic sample mean)"},
		{"Watts maximum", wattMaximum},
		{"Observer request mean", formatSeconds(power.MeanRequestLatencySeconds)},
		{"Observer request P95", formatSeconds(power.P95RequestLatencySeconds)},
		{"Maximum sample gap", formatSeconds(power.MaximumSampleGapSeconds)},
		{"Device timestamps", map[bool]string{true: "unavailable; observer receipt time used", false: "available"}[power.SourceTimestampAbsent]},
	}); err != nil {
		return err
	}

	admission := [][2]string{{"Verdict", "not evaluated"}, {"Policy", "-"}, {"Reason codes", "-"}}
	if passport.Admission != nil {
		admission = [][2]string{
			{"Verdict", passport.Admission.Verdict},
			{"Policy", passport.Admission.PolicyVersion},
			{"Reason codes", display(strings.Join(passport.Admission.ReasonCodes, ", "))},
		}
	}
	if err := writePassportSection(table, "SCIENTIFIC ADMISSION", admission); err != nil {
		return err
	}

	if err := writePassportSection(table, "EVIDENCE LINEAGE", [][2]string{
		{"Finalized at", passport.Evidence.FinalizedAt.Format(time.RFC3339Nano)},
		{"Final object", passport.Evidence.Final.Store + "/" + passport.Evidence.Final.Key},
		{"Final digest", passport.Evidence.Final.Digest},
		{"Final bytes", fmt.Sprint(passport.Evidence.Final.Size)},
		{"Raw object", passport.Evidence.Raw.Store + "/" + passport.Evidence.Raw.Key},
		{"Raw digest", passport.Evidence.Raw.Digest},
		{"Raw bytes", fmt.Sprint(passport.Evidence.Raw.Size)},
	}); err != nil {
		return err
	}

	conditions := append([]metav1.Condition(nil), run.Conditions...)
	sort.Slice(conditions, func(i, j int) bool { return conditions[i].Type < conditions[j].Type })
	if _, err := fmt.Fprintln(table, "CONDITIONS"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(table, "TYPE\tSTATUS\tREASON\tMESSAGE"); err != nil {
		return err
	}
	for _, condition := range conditions {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", condition.Type, condition.Status,
			display(condition.Reason), display(condition.Message)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writePassportSection(writer io.Writer, heading string, values [][2]string) error {
	if _, err := fmt.Fprintln(writer, heading); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(writer, "%s\t%s\n", value[0], display(value[1])); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer)
	return err
}

func formatSeconds(value float64) string {
	return fmt.Sprintf("%.6f s", value)
}

func formatMilliseconds(value float64) string {
	return fmt.Sprintf("%.3f ms", value)
}

func formatWatts(value float64) string {
	return fmt.Sprintf("%.3f W", value)
}
