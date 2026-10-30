package main

import (
	"context"
	"fmt"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	consolekube "github.com/lab-paper-code/chill/internal/operatorconsole/kube"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newModelCompatibilityQuery(
	reader client.Reader,
	opts options,
) (*consolekube.KubernetesModelCompatibilityQuery, error) {
	resolver, err := profilerkube.NewKubernetesCandidateResolver(reader, profilerkube.CandidateResolverOptions{
		Namespace: opts.namespace, ConfigMapName: opts.factSourceConfigMap, ConfigMapKey: opts.executionFactsKey,
	})
	if err != nil {
		return nil, fmt.Errorf("construct compatibility fact resolver: %w", err)
	}
	return consolekube.NewKubernetesModelCompatibilityQuery(func(
		ctx context.Context,
		selection operatorconsole.ModelCompatibilitySelection,
	) (operatorconsole.ModelCompatibilitySnapshot, error) {
		facts, err := resolver.ResolveModelCompatibility(ctx, profilerkube.ModelCompatibilitySelection{
			ModelSpec: selection.ModelSpec, ExecutionPath: selection.ExecutionPath,
			DeviceClass: selection.DeviceClass, Node: selection.Node,
		})
		if err != nil {
			return operatorconsole.ModelCompatibilitySnapshot{}, err
		}
		snapshot := operatorconsole.ModelCompatibilitySnapshot{
			Verdict: string(facts.Verdict),
			Inputs: operatorconsole.ModelCompatibilityInputs{
				ModelSpec: operatorconsole.ModelCompatibilityObjectInput{
					Name: facts.ModelSpec.Name, UID: facts.ModelSpec.UID, ResourceVersion: facts.ModelSpec.ResourceVersion,
				},
				DeviceClass: operatorconsole.ModelCompatibilityObjectInput{
					Name: facts.DeviceClass.Name, UID: facts.DeviceClass.UID, ResourceVersion: facts.DeviceClass.ResourceVersion,
				},
				Node: operatorconsole.ModelCompatibilityObjectInput{
					Name: facts.Node.Name, UID: facts.Node.UID, ResourceVersion: facts.Node.ResourceVersion,
				},
			},
		}
		if input := facts.RuntimeDeclaration; input != nil {
			snapshot.Inputs.RuntimeDeclaration = &operatorconsole.ModelCompatibilityRuntimeInput{
				ModelCompatibilityObjectInput: operatorconsole.ModelCompatibilityObjectInput{
					Name: input.Name, UID: input.UID, ResourceVersion: input.ResourceVersion,
				},
				Namespace: input.Namespace, Key: input.Key, Image: input.Image,
			}
		}
		for _, reason := range facts.Reasons {
			snapshot.Reasons = append(snapshot.Reasons, operatorconsole.ModelCompatibilityReason{
				Kind: string(reason.Kind), Code: string(reason.Code), Message: reason.Message,
			})
		}
		return snapshot, nil
	})
}
