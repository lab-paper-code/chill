package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func writeProfilingRuns(writer io.Writer, output string, view operatorconsole.ProfilingRunsView) error {
	if output == outputFormatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	return writeProfilingRunsTable(writer, view)
}

func writeProfilingRunsTable(writer io.Writer, view operatorconsole.ProfilingRunsView) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	scope := view.Namespace
	if view.AllNamespaces {
		scope = "*"
	}
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nPROFILINGRUNS_RV\t%s\nNAMESPACE\t%s\n\n",
		view.ReadAt, display(view.ResourceVersion), display(scope)); err != nil {
		return err
	}
	const runHeader = "NAMESPACE\tRUN\tMODEL\tEXECUTION_PATH\tDEVICE_CLASS\tTARGET_NODE\t" +
		"BATCH\tGENERATION\tOBSERVED"
	if _, err := fmt.Fprintln(table, runHeader); err != nil {
		return err
	}
	for _, run := range view.Runs {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\n",
			run.Namespace, run.Name, run.Spec.Candidate.ModelSpec, run.Spec.Candidate.ExecutionPath,
			run.Spec.Candidate.DeviceClass, run.Spec.TargetNode, run.Spec.Measurement.Batch,
			run.Generation, run.Status.ObservedGeneration); err != nil {
			return err
		}
	}
	const conditionHeader = "\nNAMESPACE\tRUN\tCONDITION\tSTATUS\tOBSERVED\tREASON\t" +
		"LAST_TRANSITION\tMESSAGE"
	if _, err := fmt.Fprintln(table, conditionHeader); err != nil {
		return err
	}
	for _, run := range view.Runs {
		conditions := append([]metav1.Condition(nil), run.Status.Conditions...)
		sort.Slice(conditions, func(i, j int) bool { return conditions[i].Type < conditions[j].Type })
		if len(conditions) == 0 {
			if _, err := fmt.Fprintf(table, "%s\t%s\t-\t-\t-\t-\t-\t-\n", run.Namespace, run.Name); err != nil {
				return err
			}
			continue
		}
		for _, condition := range conditions {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
				run.Namespace, run.Name, condition.Type, condition.Status, condition.ObservedGeneration,
				display(condition.Reason), condition.LastTransitionTime.UTC().Format(time.RFC3339),
				display(condition.Message)); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}
