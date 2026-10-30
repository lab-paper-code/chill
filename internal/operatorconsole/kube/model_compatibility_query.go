package kube

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// The composition root supplies the existing Profiler fact resolver without
// introducing an Operator-to-Profiler runtime dependency.
type KubernetesModelCompatibilityQuery struct {
	resolve func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error)
	now     func() time.Time
}

func NewKubernetesModelCompatibilityQuery(
	resolve func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error),
) (*KubernetesModelCompatibilityQuery, error) {
	if resolve == nil {
		return nil, errors.New("a static model compatibility resolver is required")
	}
	return &KubernetesModelCompatibilityQuery{resolve: resolve, now: time.Now}, nil
}

func (q *KubernetesModelCompatibilityQuery) Read(
	ctx context.Context,
	selection operatorconsole.ModelCompatibilitySelection,
) (operatorconsole.ModelCompatibilitySnapshot, error) {
	for _, name := range []string{selection.ModelSpec, selection.DeviceClass, selection.Node} {
		if len(validation.IsDNS1123Subdomain(name)) != 0 {
			return operatorconsole.ModelCompatibilitySnapshot{}, apierrors.NewBadRequest("modelSpec, deviceClass and node must be Kubernetes object names")
		}
	}
	if selection.ExecutionPath == "" || strings.TrimSpace(selection.ExecutionPath) != selection.ExecutionPath {
		return operatorconsole.ModelCompatibilitySnapshot{}, apierrors.NewBadRequest("executionPath is required without surrounding whitespace")
	}
	snapshot, err := q.resolve(ctx, selection)
	if err != nil {
		return operatorconsole.ModelCompatibilitySnapshot{}, err
	}
	snapshot.ReadAt, snapshot.Selection = q.now().UTC(), selection
	return snapshot, nil
}

// BuildModelCompatibilityMatrix shares one set of inputs across representative
// rows and the actual Node rows used by selected-Node coverage.
func BuildModelCompatibilityMatrix(ctx context.Context, inventory operatorconsole.InventorySnapshot, resolve func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error)) (operatorconsole.ModelCompatibilityMatrixView, error) {
	matrix := operatorconsole.ModelCompatibilityMatrixView{
		ReadAt: inventory.ReadAt.Format(time.RFC3339Nano),
		Scope: "name-sorted representative Node per DeviceClass; static support only",
		Rows: []operatorconsole.ModelCompatibilityView{}, NodeRows: []operatorconsole.ModelCompatibilityView{},
	}
	for _, model := range inventory.Models {
		for _, path := range model.Spec.ExecutionPaths {
			for _, class := range inventory.Classes {
				selection := operatorconsole.ModelCompatibilitySelection{ModelSpec: model.Name, ExecutionPath: path.Name, DeviceClass: class.Name}
				base := operatorconsole.ModelCompatibilitySnapshot{
					ReadAt: inventory.ReadAt, Selection: selection, Verdict: "Unknown",
					Inputs: operatorconsole.ModelCompatibilityInputs{
						ModelSpec: operatorconsole.ModelCompatibilityObjectInput{Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion},
						DeviceClass: operatorconsole.ModelCompatibilityObjectInput{Name: class.Name, UID: string(class.UID), ResourceVersion: class.ResourceVersion},
					},
				}
				representative := operatorconsole.BuildModelCompatibility(base)
				representative.Reasons = []operatorconsole.ModelCompatibilityReason{{Kind: "Unknown", Code: "RepresentativeNodeMissing", Message: "No Node currently matches this DeviceClass"}}
				for _, node := range class.Nodes {
					if err := ctx.Err(); err != nil {
						return operatorconsole.ModelCompatibilityMatrixView{}, err
					}
					selection.Node = node.Name
					snapshot, err := resolve(ctx, selection)
					if err != nil {
						snapshot = base
						snapshot.Selection = selection
						snapshot.Inputs.Node = operatorconsole.ModelCompatibilityObjectInput{Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion}
						snapshot.Reasons = []operatorconsole.ModelCompatibilityReason{{Kind: "Unknown", Code: "CompatibilityReadFailed", Message: "Static support could not be read for this Node"}}
					}
					row := operatorconsole.BuildModelCompatibility(snapshot)
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
		return operatorconsole.ModelCompatibilityMatrixView{}, err
	}
	return matrix, nil
}
