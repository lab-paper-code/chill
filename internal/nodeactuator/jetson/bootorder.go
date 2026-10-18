package jetson

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnsureBootOrderingCommand is the node-actuator argument that runs
// EnsureBootOrdering against the host systemd directories mounted at
// HostSystemdEtcDir and HostSystemdLibDir, then exits.
const (
	EnsureBootOrderingCommand = "ensure-jetson-boot-ordering"
	HostSystemdEtcDir         = "/host/etc/systemd/system"
	HostSystemdLibDir         = "/host/lib/systemd/system"
)

// nvidia-cdi-refresh.service (nvidia-container-toolkit) runs nvidia-smi early
// in boot and creates the GPU golden context. nvpmodel.service can no longer
// set the GPU power-gating masks after that, reports "Reboot required" and
// applies no mode, so the boot-established power modes never take effect.
// Ordering the unit after nvpmodel.service restores the boot-time apply.
const (
	cdiRefreshUnit      = "nvidia-cdi-refresh.service"
	bootOrderingDropIn  = cdiRefreshUnit + ".d/10-after-nvpmodel.conf"
	bootOrderingContent = "[Unit]\nAfter=nvpmodel.service\n"
)

// EnsureBootOrdering writes the nvidia-cdi-refresh.service drop-in under
// etcDir when either directory declares that unit, and reports whether it
// wrote. A host without the unit is left untouched. systemd reads the drop-in
// at the next boot, which is the only boot the ordering affects, so no
// daemon-reload is needed.
func EnsureBootOrdering(etcDir, libDir string) (bool, error) {
	present := false
	for _, dir := range []string{etcDir, libDir} {
		_, err := os.Lstat(filepath.Join(dir, cdiRefreshUnit))
		if err == nil {
			present = true
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("inspect %s: %w", cdiRefreshUnit, err)
		}
	}
	if !present {
		return false, nil
	}
	target := filepath.Join(etcDir, bootOrderingDropIn)
	if current, err := os.ReadFile(target); err == nil && string(current) == bootOrderingContent {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, fmt.Errorf("create %s drop-in directory: %w", cdiRefreshUnit, err)
	}
	if err := os.WriteFile(target, []byte(bootOrderingContent), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", target, err)
	}
	return true, nil
}
