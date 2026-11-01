package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
)

// opsMesh returns an initialized throwaway root (server.env + peers.db from
// the shared fixtures), a recording stubbed Cli and the call log.
func opsMesh(t *testing.T, stdin string) (string, *Cli, *recorded) {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(filepath.Join(cfg, "peers"), 0o755); err != nil {
		t.Fatal(err)
	}
	fix := "../../testdata/fixtures"
	for _, name := range []string{"server.env", "peers.db"} {
		data, err := os.ReadFile(filepath.Join(fix, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWG_MESH_ROOT", root)
	t.Setenv("AWG_MESH_NONINTERACTIVE", "1")
	// keygen shells out to docker; the fake answers with fixed keys.
	installScript(t, "docker",
		"for a in \"$@\"; do\n"+
			"  case \"$a\" in\n"+
			"    genkey) echo FAKEPRIV0000000000000000000000000000000000000A= ;;\n"+
			"    pubkey) echo FAKEPUB000000000000000000000000000000000000000B= ;;\n"+
			"    genpsk) echo FAKEPSK000000000000000000000000000000000000000C= ;;\n"+
			"  esac\n"+
			"done\n"+
			"exit 0\n")
	var stdout, stderr bytes.Buffer
	c, rec := stubbedCli(t, &stdout, &stderr)
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	return root, c, rec
}

func dbNames(t *testing.T, root string) []string {
	t.Helper()
	plist, err := peers.Load(filepath.Join(root, "config", "peers.db"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(plist))
	for _, p := range plist {
		names = append(names, p.Name)
	}
	return names
}

func sighups(rec *recorded) int {
	n := 0
	for _, call := range rec.compose {
		if call == "exec" {
			n++
		}
	}
	return n
}

func lastJSON(t *testing.T, stdout *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	var out map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &out); err != nil {
		t.Fatalf("stdout last line is not JSON: %v\n%s", err, stdout.String())
	}
	return out
}

// --- peer rename --------------------------------------------------------------

func TestRenameSinglePairShapeUnchanged(t *testing.T) {
	root, c, rec := opsMesh(t, "")
	var stdout bytes.Buffer
	c.Stdout = &stdout
	if code := c.Run([]string{"--json", "peer", "rename", "laptop-test", "laptop"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, "")
	}
	got := strings.Join(dbNames(t, root), " ")
	if got != "hub-test laptop phone-test" {
		t.Fatalf("db names: %s", got)
	}
	out := lastJSON(t, &stdout)
	if out["renamed"] != "laptop-test" || out["name"] != "laptop" {
		t.Fatalf("json: %v", out)
	}
	if sighups(rec) != 1 {
		t.Fatalf("sighups: %v", rec.compose)
	}
}

func TestRenameBulkPairsAndSwap(t *testing.T) {
	root, c, rec := opsMesh(t, "")
	var stdout bytes.Buffer
	c.Stdout = &stdout
	code := c.Run([]string{"--json", "peer", "rename",
		"hub-test", "phone-test", "phone-test", "hub-test", "laptop-test", "laptop"})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := strings.Join(dbNames(t, root), " ")
	if got != "phone-test laptop hub-test" {
		t.Fatalf("db names: %s", got)
	}
	out := lastJSON(t, &stdout)
	renamed, ok := out["renamed"].(map[string]any)
	if !ok || renamed["hub-test"] != "phone-test" ||
		renamed["phone-test"] != "hub-test" || renamed["laptop-test"] != "laptop" {
		t.Fatalf("json: %v", out)
	}
	if sighups(rec) != 1 {
		t.Fatalf("sighups: %v", rec.compose)
	}
}

func TestRenameBulkValidationLeavesDbUntouched(t *testing.T) {
	cases := []struct {
		argv []string
		msg  string
	}{
		{[]string{"peer", "rename", "laptop-test"}, "peer rename takes <old> <new> pairs"},
		{[]string{"peer", "rename", "laptop-test", "x", "ghost", "y"}, "no such peer: ghost"},
		{[]string{"peer", "rename", "laptop-test", "Bad_Name"}, "invalid peer name: Bad_Name"},
		{[]string{"peer", "rename", "laptop-test", "hub-test"}, "peer already exists: hub-test"},
		{[]string{"peer", "rename", "laptop-test", "new", "phone-test", "new"}, "duplicate new name: new"},
		{[]string{"peer", "rename", "laptop-test", "a", "laptop-test", "b"}, "peer renamed twice in one call: laptop-test"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.argv, "_"), func(t *testing.T) {
			root, c, _ := opsMesh(t, "")
			var stderr bytes.Buffer
			c.Stderr = &stderr
			before := strings.Join(dbNames(t, root), " ")
			if code := c.Run(tc.argv); code != 1 {
				t.Fatalf("code=%d want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.msg) {
				t.Fatalf("stderr %q missing %q", stderr.String(), tc.msg)
			}
			if after := strings.Join(dbNames(t, root), " "); after != before {
				t.Fatalf("db changed: %s -> %s", before, after)
			}
		})
	}
}

// --- peer rotate --------------------------------------------------------------

func TestRotateReissuesKeys(t *testing.T) {
	root, c, rec := opsMesh(t, "")
	var stdout bytes.Buffer
	c.Stdout = &stdout
	if code := c.Run([]string{"--json", "peer", "rotate", "laptop-test"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	plist, err := peers.Load(filepath.Join(root, "config", "peers.db"))
	if err != nil {
		t.Fatal(err)
	}
	var rotated *peers.Peer
	for i := range plist {
		if plist[i].Name == "laptop-test" {
			rotated = &plist[i]
		}
	}
	if rotated == nil || rotated.Pub != "FAKEPUB000000000000000000000000000000000000000B=" ||
		rotated.PSK != "FAKEPSK000000000000000000000000000000000000000C=" {
		t.Fatalf("rotate did not reissue keys: %+v", rotated)
	}
	if rotated.IP != "100.64.0.3" {
		t.Fatalf("IP must survive rotation: %s", rotated.IP)
	}
	// old pubkey must leave the daemon -> restart path, like revoke
	if strings.Join(rec.compose, ",") != "restart,restart" {
		t.Fatalf("compose calls: %v", rec.compose)
	}
	out := lastJSON(t, &stdout)
	if out["rotated"] != "laptop-test" || out["ip"] != "100.64.0.3" {
		t.Fatalf("json: %v", out)
	}
	conf, err := os.ReadFile(filepath.Join(root, "config", "peers", "laptop-test-exit.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "PrivateKey = FAKEPRIV") {
		t.Fatalf("profile not reissued:\n%s", conf)
	}
}

func TestRotateAbortsOnDecline(t *testing.T) {
	root, c, _ := opsMesh(t, "n\n")
	t.Setenv("AWG_MESH_NONINTERACTIVE", "")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if code := c.Run([]string{"peer", "rotate", "laptop-test"}); code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "aborted") {
		t.Fatalf("stderr: %s", stderr.String())
	}
	plist, _ := peers.Load(filepath.Join(root, "config", "peers.db"))
	for _, p := range plist {
		if p.Name == "laptop-test" && strings.HasPrefix(p.Pub, "FAKEPUB") {
			t.Fatal("keys must not change after abort")
		}
	}
}

func TestRotateUnknownPeer(t *testing.T) {
	_, c, _ := opsMesh(t, "")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if code := c.Run([]string{"peer", "rotate", "ghost"}); code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "no such peer: ghost") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

// --- custom dns records ---------------------------------------------------------

func TestDnsAddListRemove(t *testing.T) {
	root, c, rec := opsMesh(t, "")
	var stdout bytes.Buffer
	c.Stdout = &stdout
	if code := c.Run([]string{"--json", "dns", "add", "nas", "100.64.0.50"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	out := lastJSON(t, &stdout)
	if out["name"] != "nas.awg" || out["ip"] != "100.64.0.50" {
		t.Fatalf("json: %v", out)
	}
	data, err := os.ReadFile(filepath.Join(root, "config", "custom.hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "100.64.0.50\tnas.awg") {
		t.Fatalf("custom.hosts:\n%s", data)
	}
	if sighups(rec) != 1 {
		t.Fatalf("sighups: %v", rec.compose)
	}

	stdout.Reset()
	if code := c.Run([]string{"--json", "dns", "list"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	out = lastJSON(t, &stdout)
	records, ok := out["records"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("json: %v", out)
	}
	rec0, ok := records[0].(map[string]any)
	if !ok || rec0["name"] != "nas.awg" || rec0["ip"] != "100.64.0.50" {
		t.Fatalf("json: %v", out)
	}

	stdout.Reset()
	if code := c.Run([]string{"--json", "dns", "remove", "nas"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	out = lastJSON(t, &stdout)
	if out["removed"] != "nas.awg" {
		t.Fatalf("json: %v", out)
	}
	data, _ = os.ReadFile(filepath.Join(root, "config", "custom.hosts"))
	if strings.Contains(string(data), "nas.awg") {
		t.Fatalf("record not removed:\n%s", data)
	}
	if sighups(rec) != 2 {
		t.Fatalf("sighups: %v", rec.compose)
	}
}

func TestDnsListEmpty(t *testing.T) {
	_, c, _ := opsMesh(t, "")
	var stdout bytes.Buffer
	c.Stdout = &stdout
	if code := c.Run([]string{"dns", "list"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout.String(), "no custom records") {
		t.Fatalf("stdout: %s", stdout.String())
	}
	stdout.Reset()
	if code := c.Run([]string{"--json", "dns", "list"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	out := lastJSON(t, &stdout)
	records, ok := out["records"].([]any)
	if !ok || len(records) != 0 {
		t.Fatalf("json: %v", out)
	}
}

func TestDnsValidation(t *testing.T) {
	cases := []struct {
		argv []string
		msg  string
	}{
		{[]string{"dns", "add", "nas.awg", "100.64.0.50"}, "invalid record name: nas.awg"},
		{[]string{"dns", "add", "NAS", "100.64.0.50"}, "invalid record name: NAS"},
		{[]string{"dns", "add", "nas", "not-an-ip"}, "invalid IP: not-an-ip"},
		{[]string{"dns", "add", "nas", "fd00::1"}, "invalid IP: fd00::1 (IPv4 only)"},
		{[]string{"dns", "add", "laptop-test", "100.64.0.50"}, "'laptop-test' is already served by MagicDNS"},
		{[]string{"dns", "add", "hub", "100.64.0.50"}, "'hub' is already served"},
		{[]string{"dns", "remove", "ghost"}, "no such record: ghost.awg"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.argv, "_"), func(t *testing.T) {
			_, c, _ := opsMesh(t, "")
			var stderr bytes.Buffer
			c.Stderr = &stderr
			if code := c.Run(tc.argv); code != 1 {
				t.Fatalf("code=%d want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.msg) {
				t.Fatalf("stderr %q missing %q", stderr.String(), tc.msg)
			}
		})
	}
}

func TestDnsAddDuplicate(t *testing.T) {
	_, c, _ := opsMesh(t, "")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if code := c.Run([]string{"dns", "add", "nas", "100.64.0.50"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if code := c.Run([]string{"dns", "add", "nas", "100.64.0.51"}); code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "record already exists: nas.awg") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}
