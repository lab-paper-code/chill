package jetson

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureBootOrderingWritesDropInOnlyWhereTheUnitExists(t *testing.T) {
	t.Parallel()
	etcDir, libDir := t.TempDir(), t.TempDir()
	target := filepath.Join(etcDir, bootOrderingDropIn)

	if wrote, err := EnsureBootOrdering(etcDir, libDir); err != nil || wrote {
		t.Fatalf("host without the unit: wrote=%v err=%v", wrote, err)
	}
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatalf("host without the unit gained a drop-in directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(libDir, cdiRefreshUnit), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := EnsureBootOrdering(etcDir, libDir); err != nil || !wrote {
		t.Fatalf("packaged unit: wrote=%v err=%v", wrote, err)
	}
	if wrote, err := EnsureBootOrdering(etcDir, libDir); err != nil || wrote {
		t.Fatalf("second run is not idempotent: wrote=%v err=%v", wrote, err)
	}

	if err := os.WriteFile(target, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := EnsureBootOrdering(etcDir, libDir); err != nil || !wrote {
		t.Fatalf("drifted drop-in: wrote=%v err=%v", wrote, err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "[Unit]\nAfter=nvpmodel.service\n" {
		t.Fatalf("drop-in content = %q err=%v", content, err)
	}

	localEtc := t.TempDir()
	if err := os.WriteFile(filepath.Join(localEtc, cdiRefreshUnit), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if wrote, err := EnsureBootOrdering(localEtc, t.TempDir()); err != nil || !wrote {
		t.Fatalf("locally installed unit: wrote=%v err=%v", wrote, err)
	}
}
