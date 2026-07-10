package utils

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive
)

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := getProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}

	return output, nil
}

// LoadImageToKindClusterWithName loads a local docker image to the kind cluster
func LoadImageToKindClusterWithName(name string) error {
	kindOptions := []string{"load", "docker-image", name, "--name", kindClusterName()}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	return err
}

func kindClusterName() string {
	if value := strings.TrimSpace(os.Getenv("KIND_CLUSTER_NAME")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("KIND_CLUSTER")); value != "" {
		return value
	}
	if output, err := exec.Command("kubectl", "config", "current-context").Output(); err == nil {
		contextName := strings.TrimSpace(string(output))
		if strings.HasPrefix(contextName, "kind-") {
			return strings.TrimPrefix(contextName, "kind-")
		}
	}
	return "chill"
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// RequireKindContext prevents e2e tests from mutating a real testbed cluster.
func RequireKindContext() error {
	output, err := Run(exec.Command("kubectl", "config", "current-context"))
	if err != nil {
		return err
	}
	contextName := strings.TrimSpace(string(output))
	if !strings.HasPrefix(contextName, "kind-") {
		return fmt.Errorf("refusing to run e2e against kube context %q; expected a kind-* context", contextName)
	}
	return nil
}

func getProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.Replace(wd, "/test/e2e", "", -1)
	return wd, nil
}
