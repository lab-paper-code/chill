package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type profilingRunsDocument struct {
	ReadAt          string                 `json:"readAt"`
	ResourceVersion string                 `json:"resourceVersion"`
	Namespace       string                 `json:"namespace,omitempty"`
	AllNamespaces   bool                   `json:"allNamespaces"`
	Runs            []profilingRunDocument `json:"runs"`
}

type profilingRunDocument struct {
	Namespace         string                          `json:"namespace"`
	Name              string                          `json:"name"`
	UID               string                          `json:"uid"`
	ResourceVersion   string                          `json:"resourceVersion"`
	Generation        int64                           `json:"generation"`
	CreationTimestamp string                          `json:"creationTimestamp"`
	Spec              edgev1alpha1.ProfilingRunSpec   `json:"spec"`
	Status            edgev1alpha1.ProfilingRunStatus `json:"status"`
}

func writeProfilingRuns(writer io.Writer, output string, snapshot operatorconsole.ProfilingRunsSnapshot) error {
	document := profilingRunsOutput(snapshot)
	if output == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	}
	return writeProfilingRunsTable(writer, document)
}

func writeProfilingRunsTable(writer io.Writer, document profilingRunsDocument) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	scope := document.Namespace
	if document.AllNamespaces {
		scope = "*"
	}
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nPROFILINGRUNS_RV\t%s\nNAMESPACE\t%s\n\n",
		document.ReadAt, display(document.ResourceVersion), display(scope)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(table, "NAMESPACE\tRUN\tMODEL\tEXECUTION_PATH\tDEVICE_CLASS\tTARGET_NODE\tBATCH\tGENERATION\tOBSERVED\tINTENT_DIGEST"); err != nil {
		return err
	}
	for _, run := range document.Runs {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			run.Namespace, run.Name, run.Spec.Candidate.ModelSpec, run.Spec.Candidate.ExecutionPath,
			run.Spec.Candidate.DeviceClass, run.Spec.TargetNode, run.Spec.Measurement.Batch,
			run.Generation, run.Status.ObservedGeneration, compactDigest(run.Status.IntentDigest)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(table, "\nNAMESPACE\tRUN\tCONDITION\tSTATUS\tOBSERVED\tREASON\tLAST_TRANSITION\tMESSAGE"); err != nil {
		return err
	}
	for _, run := range document.Runs {
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

func profilingRunsOutput(snapshot operatorconsole.ProfilingRunsSnapshot) profilingRunsDocument {
	document := profilingRunsDocument{
		ReadAt: snapshot.ReadAt.Format(time.RFC3339Nano), ResourceVersion: snapshot.ResourceVersion,
		Namespace: snapshot.Namespace, AllNamespaces: snapshot.Namespace == "",
		Runs: make([]profilingRunDocument, 0, len(snapshot.Runs)),
	}
	for _, run := range snapshot.Runs {
		document.Runs = append(document.Runs, profilingRunDocument{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID), ResourceVersion: run.ResourceVersion,
			Generation: run.Generation, CreationTimestamp: run.CreationTimestamp.UTC().Format(time.RFC3339),
			Spec: run.Spec, Status: run.Status,
		})
	}
	return document
}
