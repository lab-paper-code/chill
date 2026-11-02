package kube

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/queryapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// The composition root supplies the existing Profiler fact resolver without
// introducing an Operator-to-Profiler runtime dependency.
type KubernetesModelCompatibilityQuery struct {
	resolve func(context.Context, queryapi.ModelCompatibilitySelection) (queryapi.ModelCompatibilitySnapshot, error)
	now     func() time.Time
}

func NewKubernetesModelCompatibilityQuery(
	resolve func(context.Context, queryapi.ModelCompatibilitySelection) (queryapi.ModelCompatibilitySnapshot, error),
) (*KubernetesModelCompatibilityQuery, error) {
	if resolve == nil {
		return nil, errors.New("a static model compatibility resolver is required")
	}
	return &KubernetesModelCompatibilityQuery{resolve: resolve, now: time.Now}, nil
}

func (q *KubernetesModelCompatibilityQuery) Read(
	ctx context.Context,
	selection queryapi.ModelCompatibilitySelection,
) (queryapi.ModelCompatibilitySnapshot, error) {
	for _, name := range []string{selection.ModelSpec, selection.DeviceClass, selection.Node} {
		if len(validation.IsDNS1123Subdomain(name)) != 0 {
			return queryapi.ModelCompatibilitySnapshot{}, apierrors.NewBadRequest("modelSpec, deviceClass and node must be Kubernetes object names")
		}
	}
	if selection.ExecutionPath == "" || strings.TrimSpace(selection.ExecutionPath) != selection.ExecutionPath {
		return queryapi.ModelCompatibilitySnapshot{}, apierrors.NewBadRequest("executionPath is required without surrounding whitespace")
	}
	snapshot, err := q.resolve(ctx, selection)
	if err != nil {
		return queryapi.ModelCompatibilitySnapshot{}, err
	}
	snapshot.ReadAt, snapshot.Selection = q.now().UTC(), selection
	return snapshot, nil
}

// BuildModelCompatibilityMatrix shares one set of inputs across representative
// rows and the actual Node rows used by selected-Node coverage.
func BuildModelCompatibilityMatrix(ctx context.Context, inventory queryapi.InventorySnapshot, resolve func(context.Context, queryapi.ModelCompatibilitySelection) (queryapi.ModelCompatibilitySnapshot, error)) (queryapi.ModelCompatibilityMatrixView, error) {
	matrix := queryapi.ModelCompatibilityMatrixView{
		ReadAt: inventory.ReadAt.Format(time.RFC3339Nano),
		Scope:  "name-sorted representative Node per DeviceClass; static support only",
		Rows:   []queryapi.ModelCompatibilityView{}, NodeRows: []queryapi.ModelCompatibilityView{},
	}
	for _, model := range inventory.Models {
		for _, path := range model.Spec.ExecutionPaths {
			for _, class := range inventory.Classes {
				selection := queryapi.ModelCompatibilitySelection{ModelSpec: model.Name, ExecutionPath: path.Name, DeviceClass: class.Name}
				base := queryapi.ModelCompatibilitySnapshot{
					ReadAt: inventory.ReadAt, Selection: selection, Verdict: "Unknown",
					Inputs: queryapi.ModelCompatibilityInputs{
						ModelSpec:   queryapi.ModelCompatibilityObjectInput{Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion},
						DeviceClass: queryapi.ModelCompatibilityObjectInput{Name: class.Name, UID: string(class.UID), ResourceVersion: class.ResourceVersion},
					},
				}
				representative := queryapi.BuildModelCompatibility(base)
				representative.Reasons = []queryapi.ModelCompatibilityReason{{Kind: "Unknown", Code: "RepresentativeNodeMissing", Message: "No Node currently matches this DeviceClass"}}
				for _, node := range class.Nodes {
					if err := ctx.Err(); err != nil {
						return queryapi.ModelCompatibilityMatrixView{}, err
					}
					selection.Node = node.Name
					snapshot, err := resolve(ctx, selection)
					if err != nil {
						snapshot = base
						snapshot.Selection = selection
						snapshot.Inputs.Node = queryapi.ModelCompatibilityObjectInput{Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion}
						snapshot.Reasons = []queryapi.ModelCompatibilityReason{{Kind: "Unknown", Code: "CompatibilityReadFailed", Message: "Static support could not be read for this Node"}}
					}
					row := queryapi.BuildModelCompatibility(snapshot)
					matrix.NodeRows = append(matrix.NodeRows, row)
					if representative.Selection.Node == "" || node.Name < representative.Selection.Node {
						representative = row
					}
				}
				matrix.Rows = append(matrix.Rows, representative)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return queryapi.ModelCompatibilityMatrixView{}, err
	}
	return matrix, nil
}
