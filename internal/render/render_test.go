package render

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const fixturesDir = "../../testdata/fixtures"

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(fixturesDir, "server.env"), filepath.Join(cfg, "server.env"))
	copyFile(t, filepath.Join(fixturesDir, "peers.db"), filepath.Join(cfg, "peers.db"))
	return root
}

func TestRenderMatchesLegacyRenderPyByteForByte(t *testing.T) {
	// Golden files were produced by the pre-consolidation lib/render.py
	// (commit e8dc344) on the committed synthetic fixture. The Go port must
	// reproduce them exactly.
	root := setup(t)
	if _, err := RenderAll(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"awg0.conf", "dnsmasq.conf", "start.sh"} {
		produced, err := os.ReadFile(filepath.Join(root, "config", name))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(fixturesDir, "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(produced, expected) {
			t.Fatalf("%s diverged from legacy render.py\n--- produced ---\n%s\n--- golden ---\n%s",
				name, produced, expected)
		}
	}
}

func TestAwg0ConfWrittenWith0600(t *testing.T) {
	root := setup(t)
	if _, err := RenderAll(root); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "config", "awg0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("awg0.conf mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestStartShIsExecutable(t *testing.T) {
	root := setup(t)
	if _, err := RenderAll(root); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "config", "start.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("start.sh not executable: %o", fi.Mode().Perm())
	}
}
