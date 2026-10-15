package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The producer accepts 64 MiB sealed results; allow another 4 MiB for
// the task/status projection envelope. Console JSON does not expand HTML escaping.
const maximumMeasurementResultBytes int64 = (64 + 4) << 20

type measurementOperations interface {
	Submit(context.Context) (*unstructured.Unstructured, error)
	List(context.Context) (edgev1alpha1.BatchEnergyOptimizationList, error)
	Show(context.Context) (operatorconsole.BatchEnergyOptimizationResult, error)
}

type measurementClient struct {
	opts     operationOptions
	proxy    rest.Interface
	reader   client.Reader
	resource dynamic.ResourceInterface
	manifest []byte
}

func newMeasurementOperations(opts operationOptions) (measurementOperations, error) {
	q := &measurementClient{opts: opts}
	if opts.consoleService == "" {
		reader, namespace, err := newKubernetesAccess(opts.kubeconfig, opts.context)
		if err != nil {
			return nil, err
		}
		q.reader = reader
		if q.opts.namespace == "" {
			q.opts.namespace = namespace
		}
		return q, nil
	}
	manifestNamespace := ""
	var err error
	if opts.file != "" {
		q.manifest, manifestNamespace, err = readBatchEnergyOptimizationManifest(opts.file)
		if err != nil {
			return nil, err
		}
	}
	config, defaultNamespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	if q.opts.namespace == "" {
		q.opts.namespace = manifestNamespace
	}
	if q.opts.namespace == "" {
		q.opts.namespace = defaultNamespace
	}
	if manifestNamespace != "" && manifestNamespace != q.opts.namespace {
		return nil, errors.New("manifest namespace differs from requested namespace")
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	q.proxy = clientset.CoreV1().RESTClient()
	if opts.file != "" {
		dynamicClient, err := dynamic.NewForConfig(config)
		if err != nil {
			return nil, err
		}
		q.resource = dynamicClient.Resource(schema.GroupVersionResource{
			Group: edgev1alpha1.GroupVersion.Group, Version: edgev1alpha1.GroupVersion.Version,
			Resource: "batchenergyoptimizations",
		}).Namespace(q.opts.namespace)
	}
	return q, nil
}

func (q *measurementClient) Submit(ctx context.Context) (*unstructured.Unstructured, error) {
	var task unstructured.Unstructured
	if err := json.Unmarshal(q.manifest, &task); err != nil {
		return nil, err
	}
	if task.GetAPIVersion() != edgev1alpha1.GroupVersion.String() || task.GetKind() != "BatchEnergyOptimization" {
		return nil, errors.New("manifest must be a BatchEnergyOptimization measurement task")
	}
	if task.GetNamespace() != "" && task.GetNamespace() != q.opts.namespace {
		return nil, errors.New("manifest namespace differs from requested namespace")
	}
	task.SetNamespace(q.opts.namespace)
	manifest, err := json.Marshal(&task)
	if err != nil {
		return nil, err
	}
	preflight := &serviceProxyBatchEnergyOptimizationPreflightQuery{
		restClient: q.proxy, namespace: q.opts.namespace,
		service: q.opts.consoleService, manifest: manifest,
	}
	result, err := preflight.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !result.Resolves {
		return nil, fmt.Errorf("measurement task preflight rejected: %s", result.Reason)
	}
	options := metav1.CreateOptions{}
	if q.opts.dryRun == dryRunServer {
		options.DryRun = []string{metav1.DryRunAll}
	}
	return q.resource.Create(ctx, &task, options)
}

func (q *measurementClient) List(ctx context.Context) (edgev1alpha1.BatchEnergyOptimizationList, error) {
	var tasks edgev1alpha1.BatchEnergyOptimizationList
	namespace := q.opts.namespace
	if q.opts.allNamespaces {
		namespace = ""
	}
	err := q.reader.List(ctx, &tasks, client.InNamespace(namespace))
	sort.Slice(tasks.Items, func(i, j int) bool {
		a, b := &tasks.Items[i], &tasks.Items[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return tasks, err
}

func (q *measurementClient) Show(ctx context.Context) (operatorconsole.BatchEnergyOptimizationResult, error) {
	var result operatorconsole.BatchEnergyOptimizationResult
	request := q.proxy.Get().Namespace(q.opts.namespace).Resource("services").
		Name(q.opts.consoleService+":http").SubResource("proxy").
		Suffix("api", "v1", "namespaces", q.opts.namespace, "batch-energy-optimizations", q.opts.name, "result")
	if err := readMeasurementResultJSON(ctx, request, &result); err != nil {
		return result, err
	}
	if result.APIVersion != operatorconsole.BatchEnergyOptimizationResultAPIVersion ||
		result.Kind != "BatchEnergyOptimizationResult" ||
		result.Campaign.Namespace != q.opts.namespace || result.Campaign.Name != q.opts.name {
		return result, errors.New("Console returned an unexpected measurement task identity")
	}
	return result, nil
}

func readMeasurementResultJSON(ctx context.Context, request *rest.Request, result any) error {
	stream, err := request.Stream(ctx)
	if err != nil {
		return err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumMeasurementResultBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if int64(len(payload)) > maximumMeasurementResultBytes {
		return errors.New("measurement task result response exceeds size limit")
	}
	return json.Unmarshal(payload, result)
}

func writeOperationJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func executeMeasurements(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 || (args[0] != operationSubmit && args[0] != operationList && args[0] != operationShow) {
		return fmt.Errorf("%w: measurements requires submit, list, or show", errUsage)
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
	case operationSubmit:
		task, err := q.Submit(ctx)
		if err != nil {
			return fmt.Errorf("submit measurement task: %w", err)
		}
		requestStatus := "Accepted"
		if opts.dryRun == dryRunServer {
			requestStatus = "NOT_SUBMITTED"
		}
		if opts.output == outputFormatJSON {
			return writeOperationJSON(stdout, struct {
				Request string                     `json:"request"`
				Task    *unstructured.Unstructured `json:"task"`
			}{Request: requestStatus, Task: task})
		}
		_, err = fmt.Fprintf(table, "NAMESPACE\tMEASUREMENT_TASK\tREQUEST\n%s\t%s\t%s\n",
			task.GetNamespace(), task.GetName(), requestStatus)
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
