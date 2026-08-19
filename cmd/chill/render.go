package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

func writeInventory(writer io.Writer, output string, view operatorconsole.InventoryView) error {
	if output == outputFormatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	return writeInventoryTable(writer, view)
}

func writeInventoryTable(writer io.Writer, view operatorconsole.InventoryView) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "READ_AT\t%s\nDEVICECLASSES_RV\t%s\nNODES_RV\t%s\nMODELSPECS_RV\t%s\n\n",
		view.ReadAt, display(view.DeviceClassesResourceVersion), display(view.NodesResourceVersion),
		display(view.ModelSpecsResourceVersion)); err != nil {
		return err
	}
	const header = "CLASS\tARCH\tMEMORY\tACCELERATOR\tPOWER_MODES" +
		"\tNODE\tREADY\tSCHEDULING_DISABLED"
	if _, err := fmt.Fprintln(table, header); err != nil {
		return err
	}
	for _, class := range view.Classes {
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
	if _, err := fmt.Fprintln(table, "\nMODEL\tARTIFACT\tFORMAT"); err != nil {
		return err
	}
	for _, model := range view.Models {
		artifacts := append([]edgev1alpha1.ModelArtifact(nil), model.Spec.Artifacts...)
		sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
		for _, artifact := range artifacts {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n",
				model.Name, artifact.Name, artifact.Format); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(table, "\nMODEL\tEXECUTION_PATH\tARTIFACT\tRUNTIME_FAMILY\tBACKEND"); err != nil {
		return err
	}
	for _, model := range view.Models {
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
