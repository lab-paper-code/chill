package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// checkDeploymentConfig uses the rendered process arguments and the startup decoder.
// External scheduler ConfigMaps are supplied explicitly; this path never contacts Kubernetes.
func checkDeploymentConfig(manifest, configMaps string) error {
	var deployments []appsv1.Deployment
	externalMaps := 0
	maps := map[string]corev1.ConfigMap{}
	for _, path := range []string{manifest, configMaps} {
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = func() error {
			decoder := yaml.NewYAMLOrJSONDecoder(file, 4096)
			for {
				var raw runtime.RawExtension
				if err := decoder.Decode(&raw); err != nil {
					if errors.Is(err, io.EOF) {
						return nil
					}
					return err
				}
				if len(raw.Raw) == 0 {
					continue
				}
				var kind struct{ Kind string }
				if err := json.Unmarshal(raw.Raw, &kind); err != nil {
					return err
				}
				switch kind.Kind {
				case "ConfigMap":
					var cm corev1.ConfigMap
					if err := json.Unmarshal(raw.Raw, &cm); err != nil {
						return err
					}
					if path == configMaps {
						externalMaps++
						if externalMaps > 1 {
							return errors.New("supply exactly one selected external scheduler ConfigMap")
						}
					}
					key := cm.Namespace + "/" + cm.Name
					if _, exists := maps[key]; exists {
						return fmt.Errorf("duplicate ConfigMap %s", key)
					}
					maps[key] = cm
				case "Deployment":
					if path == configMaps {
						return errors.New("external scheduler input must contain only ConfigMaps")
					}
					var deployment appsv1.Deployment
					if err := json.Unmarshal(raw.Raw, &deployment); err != nil {
						return err
					}
					deployments = append(deployments, deployment)
				default:
					if path == configMaps {
						return errors.New("external scheduler input must contain only ConfigMaps")
					}
				}
			}
		}()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, deployment := range deployments {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "serving-entrypoint" {
				continue
			}
			var opts options
			flags := flag.NewFlagSet("rendered ChillServer", flag.ContinueOnError)
			opts.bindFlags(flags)
			if err := flags.Parse(container.Args); err != nil {
				return err
			}
			if flags.NArg() != 0 {
				return errors.New("unexpected positional ChillServer arguments")
			}
			if opts.schedulerConfigMap == "" {
				continue
			}
			key := opts.servingNamespace + "/" + opts.schedulerConfigMap
			cm, exists := maps[key]
			if !exists {
				return fmt.Errorf("scheduler ConfigMap %s required in render or CHILL_SCHEDULER_CONFIGMAP_FILE", key)
			}
			if _, err := decodeSchedulerConfiguration(cm.Data[opts.schedulerConfigKey], opts.maxRequestBytes); err != nil {
				return fmt.Errorf("scheduler ConfigMap %s key %s: %w", key, opts.schedulerConfigKey, err)
			}
		}
	}
	return nil
}
