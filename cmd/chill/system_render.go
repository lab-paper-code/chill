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

func writeSystems(writer io.Writer, output string, view operatorconsole.SystemsView) error {
	if output == outputFormatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	return writeSystemsTable(writer, view)
}

func writeSystemsTable(writer io.Writer, view operatorconsole.SystemsView) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nCHILLSYSTEMS_RV\t%s\n\n",
		view.ReadAt, display(view.ResourceVersion)); err != nil {
		return err
	}
	const systemHeader = "SYSTEM\tMANAGEMENT_NAMESPACE\tGENERATION\tOBSERVED\t" +
		"NODE_AGENT\tDEVICE_DISCOVERY\tPROFILER\tEVIDENCE_STORE"
	if _, err := fmt.Fprintln(table, systemHeader); err != nil {
		return err
	}
	for _, system := range view.Systems {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%d\t%d\t%t\t%t\t%t\t%t\n",
			system.Name, display(system.Spec.ManagementNamespace), system.Generation,
			system.Status.ObservedGeneration, system.Spec.NodeAgent.Enabled,
			system.Spec.DeviceDiscovery.Enabled, system.Spec.Profiler.Enabled,
			system.Spec.EvidenceStore.Enabled); err != nil {
			return err
		}
	}
	const conditionHeader = "\nSYSTEM\tCONDITION\tSTATUS\tOBSERVED\tREASON\t" +
		"LAST_TRANSITION\tMESSAGE"
	if _, err := fmt.Fprintln(table, conditionHeader); err != nil {
		return err
	}
	for _, system := range view.Systems {
		conditions := append([]metav1.Condition(nil), system.Status.Conditions...)
		sort.Slice(conditions, func(i, j int) bool { return conditions[i].Type < conditions[j].Type })
		if len(conditions) == 0 {
			if _, err := fmt.Fprintf(table, "%s\t-\t-\t-\t-\t-\t-\n", system.Name); err != nil {
				return err
			}
			continue
		}
		for _, condition := range conditions {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
				system.Name, condition.Type, condition.Status, condition.ObservedGeneration,
				display(condition.Reason), condition.LastTransitionTime.UTC().Format(time.RFC3339),
				display(condition.Message)); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}
