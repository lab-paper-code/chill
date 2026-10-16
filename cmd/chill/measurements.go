package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

// The producer accepts 64 MiB sealed results; allow another 4 MiB for
// the task/status projection envelope. Console JSON does not expand HTML escaping.
const maximumMeasurementResultBytes int64 = (64 + 4) << 20

const measurementDeletionRequested = "DeletionRequested"

type measurementOperations interface {
	Submit(context.Context) (httpapi.MeasurementMutationResult, error)
	List(context.Context) (edgev1alpha1.BatchEnergyOptimizationList, error)
	Show(context.Context) (operatorconsole.BatchEnergyOptimizationResult, error)
	Delete(context.Context) (httpapi.MeasurementMutationResult, error)
}

type measurementClient struct {
	*ownerClient
	manifest []byte
}

func newMeasurementOperations(opts operationOptions) (measurementOperations, error) {
	var manifest []byte
	manifestNamespace := ""
	var err error
	if opts.file != "" {
		manifest, manifestNamespace, err = readBatchEnergyOptimizationManifest(opts.file)
		if err != nil {
			return nil, err
		}
	}
	config, defaultNamespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	if opts.namespace == "" {
		opts.namespace = manifestNamespace
	}
	if opts.namespace == "" {
		opts.namespace = defaultNamespace
	}
	owner, err := newOwnerHTTPSClient(opts, config)
	if err != nil {
		return nil, err
	}
	return &measurementClient{ownerClient: owner, manifest: manifest}, nil
}

func (q *measurementClient) path() string {
	return "/api/v1/namespaces/" + url.PathEscape(q.opts.namespace) + "/batch-energy-optimizations"
}

func (q *measurementClient) Submit(ctx context.Context) (httpapi.MeasurementMutationResult, error) {
	var result httpapi.MeasurementMutationResult
	path, expected := q.path(), "Accepted"
	if q.opts.dryRun == dryRunServer {
		path += "?dryRun=All"
		expected = "NOT_SUBMITTED"
	}
	if err := q.read(ctx, http.MethodPost, path, q.manifest, "", maximumMeasurementResultBytes, &result); err != nil {
		return result, err
	}
	if result.Request != expected {
		return result, fmt.Errorf("owner did not accept measurement request: %s", result.Request)
	}
	if result.Task == nil || result.Task.GetNamespace() != q.opts.namespace {
		return result, errors.New("owner returned an unexpected measurement task identity")
	}
	return result, nil
}

func (q *measurementClient) List(ctx context.Context) (edgev1alpha1.BatchEnergyOptimizationList, error) {
	var tasks edgev1alpha1.BatchEnergyOptimizationList
	err := q.read(ctx, http.MethodGet, q.path(), nil, "", maximumMeasurementResultBytes, &tasks)
	if err == nil && (tasks.APIVersion != edgev1alpha1.GroupVersion.String() ||
		tasks.Kind != "BatchEnergyOptimizationList" || tasks.Items == nil) {
		return tasks, errors.New("owner returned an unexpected measurement task list")
	}
	return tasks, err
}

func (q *measurementClient) Show(ctx context.Context) (operatorconsole.BatchEnergyOptimizationResult, error) {
	var result operatorconsole.BatchEnergyOptimizationResult
	path := q.path() + "/" + url.PathEscape(q.opts.name) + "/result"
	if err := q.read(ctx, http.MethodGet, path, nil, "", maximumMeasurementResultBytes, &result); err != nil {
		return result, err
	}
	if result.APIVersion != operatorconsole.BatchEnergyOptimizationResultAPIVersion ||
		result.Kind != "BatchEnergyOptimizationResult" ||
		result.Campaign.Namespace != q.opts.namespace || result.Campaign.Name != q.opts.name {
		return result, errors.New("owner returned an unexpected measurement task identity")
	}
	return result, nil
}

func (q *measurementClient) Delete(ctx context.Context) (httpapi.MeasurementMutationResult, error) {
	var result httpapi.MeasurementMutationResult
	path := q.path() + "/" + url.PathEscape(q.opts.name) + "?uid=" + url.QueryEscape(q.opts.uid)
	if err := q.read(ctx, http.MethodDelete, path, nil, q.opts.ifMatch,
		maximumMeasurementResultBytes, &result); err != nil {
		return result, err
	}
	if result.Request != measurementDeletionRequested || !result.EvidenceDeleted {
		return result, fmt.Errorf(
			"owner did not complete evidence cleanup and accept measurement deletion: %s", result.Request,
		)
	}
	if result.Task == nil || result.Task.GetNamespace() != q.opts.namespace ||
		result.Task.GetName() != q.opts.name || string(result.Task.GetUID()) != q.opts.uid {
		return result, errors.New("owner returned an unexpected measurement task identity")
	}
	return result, nil
}

func writeOperationJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func executeMeasurements(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: measurements requires submit, list, show, or delete", errUsage)
	}
	switch args[0] {
	case operationSubmit, operationList, operationShow, operationDelete:
	default:
		return fmt.Errorf("%w: measurements requires submit, list, show, or delete", errUsage)
	}
	opts, err := parseOperationOptions("measurements", args[0], args[1:], stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	q, err := deps.newMeasurements(opts)
	if err != nil {
		return err
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	switch args[0] {
	case operationDelete:
		result, err := q.Delete(ctx)
		if err != nil {
			return fmt.Errorf("delete measurement task: %w", err)
		}
		if opts.output == outputFormatJSON {
			return writeOperationJSON(stdout, result)
		}
		const header = "NAMESPACE\tMEASUREMENT_TASK\tREQUEST\tEVIDENCE_DELETED\tOWNED_KEYS\n"
		if _, err := fmt.Fprintf(table, header+"%s\t%s\t%s\t%t\t%d\n",
			result.Task.GetNamespace(), result.Task.GetName(), result.Request,
			result.EvidenceDeleted, len(result.DeletedEvidence)); err != nil {
			return err
		}
	case operationSubmit:
		result, err := q.Submit(ctx)
		if err != nil {
			return fmt.Errorf("submit measurement task: %w", err)
		}
		if opts.output == outputFormatJSON {
			return writeOperationJSON(stdout, result)
		}
		_, err = fmt.Fprintf(table, "NAMESPACE\tMEASUREMENT_TASK\tREQUEST\n%s\t%s\t%s\n",
			result.Task.GetNamespace(), result.Task.GetName(), result.Request)
		if err != nil {
			return err
		}
	case operationList:
		tasks, err := q.List(ctx)
		if err != nil {
			return fmt.Errorf("list measurement tasks: %w", err)
		}
		if opts.output == outputFormatJSON {
			return writeOperationJSON(stdout, tasks)
		}
		if _, err := fmt.Fprintln(table, "NAMESPACE\tMEASUREMENT_TASK\tPHASE\tTRIALS\tOUTCOME"); err != nil {
			return err
		}
		for i := range tasks.Items {
			task := &tasks.Items[i]
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\n",
				task.Namespace, task.Name, display(task.Status.Phase),
				task.Status.CompletedTrials, display(task.Status.Outcome)); err != nil {
				return err
			}
		}
	case operationShow:
		result, err := q.Show(ctx)
		if err != nil {
			return fmt.Errorf("show measurement task: %w", err)
		}
		if opts.output == outputFormatJSON {
			return writeOperationJSON(stdout, result)
		}
		task := result.Campaign
		const taskHeader = "NAMESPACE\tMEASUREMENT_TASK\tPHASE\tTRIALS\tOUTCOME\n"
		if _, err := fmt.Fprintf(table, taskHeader+"%s\t%s\t%s\t%d\t%s\n",
			task.Namespace, task.Name, display(task.Phase), task.CompletedTrials, display(task.Outcome)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(table, "\nCONDITION\tSTATUS\tREASON\tMESSAGE"); err != nil {
			return err
		}
		for _, condition := range task.Conditions {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n",
				condition.Type, condition.Status, condition.Reason, condition.Message); err != nil {
				return err
			}
		}
		if task.StoredResult != nil {
			if _, err := fmt.Fprintf(table, "\nRESULT_STORE\tRESULT_KEY\tSIZE\n%s\t%s\t%d\n",
				task.StoredResult.Store, task.StoredResult.Key, task.StoredResult.Size); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}
