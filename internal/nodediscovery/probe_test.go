package nodediscovery

import (
	"os"
	"path/filepath"
	"testing"

	chilllabels "github.com/lab-paper-code/chill/internal/labels"
)

func TestProbeDetectsJetsonOrinNano(t *testing.T) {
	hostRoot := t.TempDir()
	writeHostFile(t, hostRoot, "proc/device-tree/model", "NVIDIA Jetson Orin Nano Developer Kit\x00")
	writeHostFile(t, hostRoot, "etc/nv_tegra_release", "# R36 (release), REVISION: 4.0")

	facts, err := Probe(hostRoot, testSignatureCatalog())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	if facts.Vendor != "nvidia" {
		t.Fatalf("Vendor = %q, want nvidia", facts.Vendor)
	}
	if facts.Family != "jetson" {
		t.Fatalf("Family = %q, want jetson", facts.Family)
	}
	if facts.Model != "orin-nano" {
		t.Fatalf("Model = %q, want orin-nano", facts.Model)
	}
	if facts.Accelerator != "nvidia-jetson-orin-nano" {
		t.Fatalf("Accelerator = %q, want nvidia-jetson-orin-nano", facts.Accelerator)
	}

	gotLabels := facts.Labels()
	if gotLabels[chilllabels.DeviceModel] != "orin-nano" {
		t.Fatalf("DeviceModel label = %q, want orin-nano", gotLabels[chilllabels.DeviceModel])
	}
	if gotLabels[chilllabels.Accelerator] != "nvidia-jetson-orin-nano" {
		t.Fatalf("Accelerator label = %q, want nvidia-jetson-orin-nano", gotLabels[chilllabels.Accelerator])
	}
}

func TestProbeDetectsLattePandaFromDMI(t *testing.T) {
	hostRoot := t.TempDir()
	writeHostFile(t, hostRoot, "sys/devices/virtual/dmi/id/sys_vendor", "LattePanda")
	writeHostFile(t, hostRoot, "sys/devices/virtual/dmi/id/product_name", "LattePanda 3 Delta")

	facts, err := Probe(hostRoot, testSignatureCatalog())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	if facts.Vendor != "lattepanda" {
		t.Fatalf("Vendor = %q, want lattepanda", facts.Vendor)
	}
	if facts.Model != "lattepanda-3-delta" {
		t.Fatalf("Model = %q, want lattepanda-3-delta", facts.Model)
	}
	if facts.Accelerator != "none" {
		t.Fatalf("Accelerator = %q, want none", facts.Accelerator)
	}
}

func TestProbeLeavesUnknownDeviceUnlabeled(t *testing.T) {
	hostRoot := t.TempDir()
	writeHostFile(t, hostRoot, "sys/devices/virtual/dmi/id/product_name", "Generic PC")

	facts, err := Probe(hostRoot, testSignatureCatalog())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	if len(facts.Labels()) != 0 {
		t.Fatalf("Labels() = %#v, want empty", facts.Labels())
	}
	if facts.Annotations()[chilllabels.DeviceModelRaw] != "Generic PC" {
		t.Fatalf("raw model annotation = %q, want Generic PC", facts.Annotations()[chilllabels.DeviceModelRaw])
	}
	if facts.Annotations()[chilllabels.NodeDiscoveryReason] != chilllabels.DiscoveryReasonNoSignatureMatch {
		t.Fatalf("node discovery reason = %q, want %q", facts.Annotations()[chilllabels.NodeDiscoveryReason], chilllabels.DiscoveryReasonNoSignatureMatch)
	}
}

func TestProbeAnnotatesNoSourceFacts(t *testing.T) {
	facts, err := Probe(t.TempDir(), SignatureCatalog{})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	annotations := facts.Annotations()
	if annotations[chilllabels.NodeDiscoveryResult] != chilllabels.DiscoveryResultUnmatched {
		t.Fatalf("node discovery result = %q, want %q", annotations[chilllabels.NodeDiscoveryResult], chilllabels.DiscoveryResultUnmatched)
	}
	if annotations[chilllabels.NodeDiscoveryReason] != chilllabels.DiscoveryReasonNoSourceFacts {
		t.Fatalf("node discovery reason = %q, want %q", annotations[chilllabels.NodeDiscoveryReason], chilllabels.DiscoveryReasonNoSourceFacts)
	}
}

func TestLoadSignatureCatalog(t *testing.T) {
	hostRoot := t.TempDir()
	path := filepath.Join(hostRoot, "signatures.yaml")
	if err := os.WriteFile(path, []byte(`signatures:
- contains: ["raspberry pi 5"]
  facts:
    vendor: raspberrypi
    family: raspberry-pi
    model: raspberry-pi-5
    accelerator: none
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	catalog, err := LoadSignatureCatalog(path)
	if err != nil {
		t.Fatalf("LoadSignatureCatalog() error = %v", err)
	}
	if len(catalog.Signatures) != 1 {
		t.Fatalf("signatures = %d, want 1", len(catalog.Signatures))
	}
	if catalog.Signatures[0].Facts.Model != "raspberry-pi-5" {
		t.Fatalf("model = %q, want raspberry-pi-5", catalog.Signatures[0].Facts.Model)
	}
}

func testSignatureCatalog() SignatureCatalog {
	return SignatureCatalog{
		Sources: []Source{
			{Paths: []string{"proc/device-tree/model", "sys/firmware/devicetree/base/model"}},
			{Paths: []string{"sys/devices/virtual/dmi/id/sys_vendor", "sys/class/dmi/id/sys_vendor"}},
			{Paths: []string{"sys/devices/virtual/dmi/id/product_name", "sys/class/dmi/id/product_name"}},
		},
		Signatures: []Signature{
			{
				Contains: []string{"nvidia jetson", "orin nano"},
				Facts: Facts{
					Vendor:      "nvidia",
					Family:      "jetson",
					Model:       "orin-nano",
					Accelerator: "nvidia-jetson-orin-nano",
				},
			},
			{
				Contains: []string{"lattepanda 3 delta"},
				Facts: Facts{
					Vendor:      "lattepanda",
					Family:      "lattepanda",
					Model:       "lattepanda-3-delta",
					Accelerator: "none",
				},
			},
		},
	}
}

func writeHostFile(t *testing.T, hostRoot, path, value string) {
	t.Helper()
	fullPath := filepath.Join(hostRoot, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(value), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
