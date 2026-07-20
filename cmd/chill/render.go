package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

type inventoryDocument struct {
	ReadAt                       string                   `json:"readAt"`
	DeviceClassesResourceVersion string                   `json:"deviceClassesResourceVersion"`
	NodesResourceVersion         string                   `json:"nodesResourceVersion"`
	ModelSpecsResourceVersion    string                   `json:"modelSpecsResourceVersion"`
	Classes                      []inventoryClassDocument `json:"classes"`
	Models                       []inventoryModelDocument `json:"models"`
}

type inventoryClassDocument struct {
	Name            string                  `json:"name"`
	UID             string                  `json:"uid"`
	ResourceVersion string                  `json:"resourceVersion"`
	NodeSelector    map[string]string       `json:"nodeSelector"`
	Architecture    string                  `json:"architecture"`
	Memory          string                  `json:"memory"`
	Accelerator     string                  `json:"accelerator,omitempty"`
	PowerModes      []powerModeDocument     `json:"powerModes"`
	Nodes           []inventoryNodeDocument `json:"nodes"`
}

type powerModeDocument struct {
	Name     string                         `json:"name"`
	Actuator edgev1alpha1.PowerModeActuator `json:"actuator"`
}

type inventoryNodeDocument struct {
	Name               string `json:"name"`
	UID                string `json:"uid"`
	ResourceVersion    string `json:"resourceVersion"`
	Ready              string `json:"ready"`
	SchedulingDisabled bool   `json:"schedulingDisabled"`
}

type inventoryModelDocument struct {
	Name            string                     `json:"name"`
	UID             string                     `json:"uid"`
	ResourceVersion string                     `json:"resourceVersion"`
	Spec            edgev1alpha1.ModelSpecSpec `json:"spec"`
}

func writeInventory(writer io.Writer, output string, snapshot operatorconsole.InventorySnapshot) error {
	document := inventoryOutput(snapshot)
	if output == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	}
	return writeInventoryTable(writer, document)
}

func writeInventoryTable(writer io.Writer, document inventoryDocument) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nDEVICECLASSES_RV\t%s\nNODES_RV\t%s\nMODELSPECS_RV\t%s\n\n",
		document.ReadAt, display(document.DeviceClassesResourceVersion), display(document.NodesResourceVersion),
		display(document.ModelSpecsResourceVersion)); err != nil {
		return err
	}
	const header = "CLASS\tARCH\tMEMORY\tACCELERATOR\tPOWER_MODES" +
		"\tNODE\tREADY\tSCHEDULING_DISABLED"
	if _, err := fmt.Fprintln(table, header); err != nil {
		return err
	}
	for _, class := range document.Classes {
		powerModes := make([]string, 0, len(class.PowerModes))
		for _, mode := range class.PowerModes {
			powerModes = append(powerModes, mode.Name)
		}
		if len(class.Nodes) == 0 {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t-\t-\t-\n",
				class.Name, class.Architecture, class.Memory, display(class.Accelerator),
				display(strings.Join(powerModes, ","))); err != nil {
				return err
			}
			continue
		}
		for _, node := range class.Nodes {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\n",
				class.Name, class.Architecture, class.Memory, display(class.Accelerator),
				display(strings.Join(powerModes, ",")), node.Name, node.Ready, node.SchedulingDisabled); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(table, "\nMODEL\tARTIFACT\tFORMAT\tDIGEST"); err != nil {
		return err
	}
	for _, model := range document.Models {
		artifacts := append([]edgev1alpha1.ModelArtifact(nil), model.Spec.Artifacts...)
		sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
		for _, artifact := range artifacts {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n",
				model.Name, artifact.Name, artifact.Format, compactDigest(artifact.Digest)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(table, "\nMODEL\tEXECUTION_PATH\tARTIFACT\tRUNTIME_FAMILY\tBACKEND"); err != nil {
		return err
	}
	for _, model := range document.Models {
		paths := append([]edgev1alpha1.ModelExecutionPath(nil), model.Spec.ExecutionPaths...)
		sort.Slice(paths, func(i, j int) bool { return paths[i].Name < paths[j].Name })
		for _, path := range paths {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
				model.Name, path.Name, path.Artifact, path.Runtime.Family, path.Runtime.Backend); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

func inventoryOutput(snapshot operatorconsole.InventorySnapshot) inventoryDocument {
	document := inventoryDocument{
		ReadAt:                       snapshot.ReadAt.Format(time.RFC3339Nano),
		DeviceClassesResourceVersion: snapshot.DeviceClassesResourceVersion,
		NodesResourceVersion:         snapshot.NodesResourceVersion,
		ModelSpecsResourceVersion:    snapshot.ModelSpecsResourceVersion,
		Classes:                      make([]inventoryClassDocument, 0, len(snapshot.Classes)),
		Models:                       make([]inventoryModelDocument, 0, len(snapshot.Models)),
	}
	for _, class := range snapshot.Classes {
		document.Classes = append(document.Classes, inventoryClassOutput(class))
	}
	for _, model := range snapshot.Models {
		document.Models = append(document.Models, inventoryModelDocument{
			Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion, Spec: model.Spec,
		})
	}
	return document
}

func inventoryClassOutput(class deviceclass.InventoryClass) inventoryClassDocument {
	document := inventoryClassDocument{
		Name: class.Name, UID: string(class.UID), ResourceVersion: class.ResourceVersion,
		NodeSelector: class.Spec.NodeSelector, Architecture: class.Spec.Architecture,
		Memory: class.Spec.MemoryBytes.String(), Accelerator: class.Spec.Accelerator,
		PowerModes: make([]powerModeDocument, 0, len(class.Spec.PowerModes)),
		Nodes:      make([]inventoryNodeDocument, 0, len(class.Nodes)),
	}
	for _, mode := range class.Spec.PowerModes {
		document.PowerModes = append(document.PowerModes, powerModeDocument{Name: mode.Name, Actuator: mode.Actuator})
	}
	for _, node := range class.Nodes {
		document.Nodes = append(document.Nodes, inventoryNodeDocument{
			Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion,
			Ready: string(node.Ready), SchedulingDisabled: node.SchedulingDisabled,
		})
	}
	return document
}
