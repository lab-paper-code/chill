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

type systemsDocument struct {
	ReadAt          string           `json:"readAt"`
	ResourceVersion string           `json:"resourceVersion"`
	Systems         []systemDocument `json:"systems"`
}

type systemDocument struct {
	Name              string                         `json:"name"`
	UID               string                         `json:"uid"`
	ResourceVersion   string                         `json:"resourceVersion"`
	Generation        int64                          `json:"generation"`
	CreationTimestamp string                         `json:"creationTimestamp"`
	Spec              edgev1alpha1.ChillSystemSpec   `json:"spec"`
	Status            edgev1alpha1.ChillSystemStatus `json:"status"`
}

func writeSystems(writer io.Writer, output string, snapshot operatorconsole.SystemSnapshot) error {
	document := systemsOutput(snapshot)
	if output == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	}
	return writeSystemsTable(writer, document)
}

func writeSystemsTable(writer io.Writer, document systemsDocument) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nCHILLSYSTEMS_RV\t%s\n\n",
		document.ReadAt, display(document.ResourceVersion)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(table, "SYSTEM\tMANAGEMENT_NAMESPACE\tGENERATION\tOBSERVED\tNODE_DISCOVERY\tDEVICE_DISCOVERY\tPROFILER\tEVIDENCE_STORE"); err != nil {
		return err
	}
	for _, system := range document.Systems {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%d\t%d\t%t\t%t\t%t\t%t\n",
			system.Name, display(system.Spec.ManagementNamespace), system.Generation,
			system.Status.ObservedGeneration, system.Spec.NodeDiscovery.Enabled,
			system.Spec.DeviceDiscovery.Enabled, system.Spec.Profiler.Enabled,
			system.Spec.EvidenceStore.Enabled); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(table, "\nSYSTEM\tCONDITION\tSTATUS\tOBSERVED\tREASON\tLAST_TRANSITION\tMESSAGE"); err != nil {
		return err
	}
	for _, system := range document.Systems {
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

func systemsOutput(snapshot operatorconsole.SystemSnapshot) systemsDocument {
	document := systemsDocument{
		ReadAt: snapshot.ReadAt.Format(time.RFC3339Nano), ResourceVersion: snapshot.ResourceVersion,
		Systems: make([]systemDocument, 0, len(snapshot.Systems)),
	}
	for _, system := range snapshot.Systems {
		document.Systems = append(document.Systems, systemDocument{
			Name: system.Name, UID: string(system.UID), ResourceVersion: system.ResourceVersion,
			Generation: system.Generation, CreationTimestamp: system.CreationTimestamp.UTC().Format(time.RFC3339),
			Spec: system.Spec, Status: system.Status,
		})
	}
	return document
}
