package main

import (
	"context"
	"fmt"

	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newModelCompatibilityQuery(
	reader client.Reader,
	opts options,
) (*querykube.KubernetesModelCompatibilityQuery, error) {
	resolver, err := profilerkube.NewKubernetesCandidateResolver(reader, profilerkube.CandidateResolverOptions{
		Namespace: opts.namespace, ConfigMapName: opts.factSourceConfigMap, ConfigMapKey: opts.executionFactsKey,
	})
	if err != nil {
		return nil, fmt.Errorf("construct compatibility fact resolver: %w", err)
	}
	return querykube.NewKubernetesModelCompatibilityQuery(func(
		ctx context.Context,
		selection queryapi.ModelCompatibilitySelection,
	) (queryapi.ModelCompatibilitySnapshot, error) {
		facts, err := resolver.ResolveModelCompatibility(ctx, profilerkube.ModelCompatibilitySelection{
			ModelSpec: selection.ModelSpec, ExecutionPath: selection.ExecutionPath,
			DeviceClass: selection.DeviceClass, Node: selection.Node,
		})
		if err != nil {
			return queryapi.ModelCompatibilitySnapshot{}, err
		}
		snapshot := queryapi.ModelCompatibilitySnapshot{
			Verdict: string(facts.Verdict), Diagnostics: facts.Diagnostics,
			Inputs: queryapi.ModelCompatibilityInputs{
				ModelSpec: queryapi.ModelCompatibilityObjectInput{
					Name: facts.ModelSpec.Name, UID: facts.ModelSpec.UID, ResourceVersion: facts.ModelSpec.ResourceVersion,
				},
				DeviceClass: queryapi.ModelCompatibilityObjectInput{
					Name: facts.DeviceClass.Name, UID: facts.DeviceClass.UID, ResourceVersion: facts.DeviceClass.ResourceVersion,
				},
				Node: queryapi.ModelCompatibilityObjectInput{
					Name: facts.Node.Name, UID: facts.Node.UID, ResourceVersion: facts.Node.ResourceVersion,
				},
			},
		}
		if input := facts.RuntimeDeclaration; input != nil {
			snapshot.Inputs.RuntimeDeclaration = &queryapi.ModelCompatibilityRuntimeInput{
				ModelCompatibilityObjectInput: queryapi.ModelCompatibilityObjectInput{
					Name: input.Name, UID: input.UID, ResourceVersion: input.ResourceVersion,
				},
				Namespace: input.Namespace, Key: input.Key, Image: input.Image,
			}
		}
		for _, reason := range facts.Reasons {
			snapshot.Reasons = append(snapshot.Reasons, queryapi.ModelCompatibilityReason{
				Kind: string(reason.Kind), Code: string(reason.Code), Message: reason.Message,
			})
		}
		return snapshot, nil
	})
}
