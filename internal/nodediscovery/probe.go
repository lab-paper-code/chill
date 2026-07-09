package nodediscovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lab-paper-code/chill/internal/chilllabels"
	"sigs.k8s.io/yaml"
)

// Facts are hardware facts discovered on one Kubernetes node.
type Facts struct {
	Vendor      string `json:"vendor,omitempty"`
	Family      string `json:"family,omitempty"`
	Model       string `json:"model,omitempty"`
	Accelerator string `json:"accelerator,omitempty"`
	RawModel    string `json:"rawModel,omitempty"`
}

// SignatureCatalog contains data-driven node hardware matching rules.
type SignatureCatalog struct {
	Sources    []Source    `json:"sources,omitempty"`
	Signatures []Signature `json:"signatures"`
}

// Source describes host files whose text is used for signature matching.
type Source struct {
	Paths []string `json:"paths"`
}

// Signature maps substrings found in host facts to normalized CHILL facts.
type Signature struct {
	Contains []string `json:"contains"`
	Facts    Facts    `json:"facts"`
}

// Probe reads host files and returns the normalized hardware facts CHILL needs.
func Probe(hostRoot string, catalog SignatureCatalog) (Facts, error) {
	probe := filesystemProbe{
		hostRoot: hostRoot,
		catalog:  catalog,
	}
	return probe.run()
}

// LoadSignatureCatalog reads hardware signature rules from a YAML file.
func LoadSignatureCatalog(path string) (SignatureCatalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SignatureCatalog{}, err
	}
	var catalog SignatureCatalog
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		return SignatureCatalog{}, fmt.Errorf("parse node discovery signatures %q: %w", path, err)
	}
	return catalog, nil
}

// Labels returns Kubernetes node labels for the discovered facts.
func (f Facts) Labels() map[string]string {
	labels := map[string]string{}
	addLabel(labels, chilllabels.DeviceVendor, f.Vendor)
	addLabel(labels, chilllabels.DeviceFamily, f.Family)
	addLabel(labels, chilllabels.DeviceModel, f.Model)
	addLabel(labels, chilllabels.Accelerator, f.Accelerator)
	return labels
}

// Annotations returns Kubernetes node annotations for non-selector discovery details.
func (f Facts) Annotations() map[string]string {
	annotations := map[string]string{}
	if f.RawModel != "" {
		annotations[chilllabels.DeviceModelRaw] = f.RawModel
	}
	if len(f.Labels()) > 0 || f.RawModel != "" {
		annotations[chilllabels.DiscoverySource] = chilllabels.SourceNodeDiscovery
	}
	return annotations
}

func addLabel(labels map[string]string, key, value string) {
	value = sanitizeLabelValue(value)
	if value != "" {
		labels[key] = value
	}
}

type filesystemProbe struct {
	hostRoot string
	catalog  SignatureCatalog
}

func (p filesystemProbe) run() (Facts, error) {
	values, err := p.readSources()
	if err != nil {
		return Facts{}, err
	}

	rawModel := firstNonEmpty(values...)
	haystack := normalizeSearchText(values...)
	facts := p.catalog.detect(haystack)
	facts.RawModel = rawModel
	return facts, nil
}

func (p filesystemProbe) readSources() ([]string, error) {
	values := make([]string, 0, len(p.catalog.Sources))
	for _, source := range p.catalog.Sources {
		value, err := p.firstExistingText(source.Paths...)
		if err != nil {
			return nil, err
		}
		if value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

func (p filesystemProbe) firstExistingText(paths ...string) (string, error) {
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(p.hostRoot, path))
		if err == nil {
			return cleanHostText(string(data)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

func (c SignatureCatalog) detect(haystack string) Facts {
	for _, signature := range c.Signatures {
		if signature.matches(haystack) {
			return signature.Facts
		}
	}
	return Facts{}
}

func (s Signature) matches(haystack string) bool {
	for _, needle := range s.Contains {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func normalizeSearchText(values ...string) string {
	return strings.ToLower(strings.Join(values, " "))
}

func cleanHostText(value string) string {
	value = strings.ReplaceAll(value, "\x00", " ")
	return strings.Join(strings.Fields(value), " ")
}

var labelValueCleanup = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func sanitizeLabelValue(value string) string {
	value = labelValueCleanup.ReplaceAllString(strings.TrimSpace(value), "-")
	value = strings.Trim(value, "-_.")
	if len(value) > 63 {
		value = strings.Trim(value[:63], "-_.")
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
