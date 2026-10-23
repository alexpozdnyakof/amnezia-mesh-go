package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/compose"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/doctor"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
)

const goldenConf = "../../testdata/fixtures/golden/awg0.conf"

func mustReadGolden(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(goldenConf)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// stubbedCli returns a Cli with docker/compose/doctor/pubkey seams and the
// recorded compose calls + docker calls.
type recorded struct {
	compose []string
	docker  [][]string
	events  []string // interleaved compose/docker actions in call order
}

func stubbedCli(t *testing.T, stdout, stderr *bytes.Buffer) (*Cli, *recorded) {
	t.Helper()
	rec := &recorded{}
	c := New(strings.NewReader(""), stdout, stderr)
	c.ComposeRun = func(root string, env map[string]string, stdin string, args ...string) compose.Result {
		rec.compose = append(rec.compose, args[0])
		rec.events = append(rec.events, args[0])
		return compose.Result{}
	}
	c.DoctorRunChecks = func(root string) []doctor.Check {
		return []doctor.Check{{Name: "all", OK: true, Detail: "stub"}}
	}
	c.KeygenPubFromPriv = func(image, priv string) (string, error) {
		return strings.Repeat("D", 44), nil
	}
	c.DockerRun = func(args ...string) (string, string, int) {
		rec.docker = append(rec.docker, args)
		if args[0] == "stop" || args[0] == "start" {
			rec.events = append(rec.events, args[0])
		}
		return "", "", 0
	}
	return c, rec
}

func TestCmdAdoptConfigSourceSuccess(t *testing.T) {
	rootDir := t.TempDir()
	t.Setenv("AWG_MESH_ROOT", rootDir)
	t.Setenv("AWG_MESH_NONINTERACTIVE", "1")
	var stdout, stderr bytes.Buffer
	c, rec := stubbedCli(t, &stdout, &stderr)

	code := c.Run([]string{"--json", "adopt", "--config", goldenConf,
		"--image", "img:latest", "--host", "192.0.2.10",
		"--port", "443", "--dns2", "1.0.0.1", "--rdp-reject"})

	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, stderr.String())
	}
	var out struct {
		OK     bool `json:"ok"`
		Peers  int  `json:"peers"`
		Subnet string
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if !out.OK || out.Peers != 3 {
		t.Fatalf("unexpected result: %+v", out)
	}
	envFile := filepath.Join(rootDir, "config", "server.env")
	fi, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server.env mode = %o, want 0600", fi.Mode().Perm())
	}
	// no container source -> no docker stop/start, compose up ran, doctor ran
	if len(rec.docker) != 0 {
		t.Fatalf("no docker calls expected: %v", rec.docker)
	}
	if len(rec.compose) != 1 || rec.compose[0] != "up" {
		t.Fatalf("compose calls = %v, want [up]", rec.compose)
	}
}

func TestCmdAdoptContainerSourceOrderAndRollback(t *testing.T) {
	rootDir := t.TempDir()
	t.Setenv("AWG_MESH_ROOT", rootDir)
	t.Setenv("AWG_MESH_NONINTERACTIVE", "1")
	confText := mustReadGolden(t)
	var stdout, stderr bytes.Buffer
	c, rec := stubbedCli(t, &stdout, &stderr)
	c.DoctorRunChecks = func(root string) []doctor.Check {
		return []doctor.Check{{Name: "x", OK: false, Detail: "boom"}}
	}
	c.DockerRun = func(args ...string) (string, string, int) {
		rec.docker = append(rec.docker, args)
		if args[0] == "stop" || args[0] == "start" {
			rec.events = append(rec.events, args[0])
		}
		switch args[0] {
		case "exec":
			return confText, "", 0
		case "inspect":
			return "amneziavpn/amneziawg-go:latest\n", "", 0
		}
		return "", "", 0
	}

	code := c.Run([]string{"adopt", "--container", "old-awg",
		"--host", "192.0.2.10", "--port", "443"})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	var kinds []string
	for _, kind := range rec.events {
		kinds = append(kinds, kind)
	}
	// Order matters: stop old -> up -> (doctor fails) down -> start old.
	want := []string{"stop", "up", "down", "start"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("sequence = %v, want %v (compose=%v docker=%v)", kinds, want, rec.compose, rec.docker)
	}
}

// TestPeerRenameAndRevokeFlow exercises peer rename/revoke against a real
// temp config: db mutations, re-render and the apply-conf compose calls.
func TestPeerRenameAndRevokeFlow(t *testing.T) {
	rootDir := t.TempDir()
	t.Setenv("AWG_MESH_ROOT", rootDir)
	t.Setenv("AWG_MESH_NONINTERACTIVE", "1")
	cfg := filepath.Join(rootDir, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	envData, err := os.ReadFile("../../testdata/fixtures/server.env")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "server.env"), envData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := peers.WithLock(filepath.Join(cfg, "peers.db"), func(tx *peers.Tx) error {
		tx.Add(peers.Peer{Name: "phone", IP: "100.64.0.2", Pub: "p", PSK: "k", Created: "2026-10-24T00:00:00Z"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	c, rec := stubbedCli(t, &stdout, &stderr)

	if code := c.Run([]string{"peer", "rename", "phone", "phone2"}); code != 0 {
		t.Fatalf("rename code = %d, stderr: %s", code, stderr.String())
	}
	plist, _ := peers.Load(filepath.Join(cfg, "peers.db"))
	if len(plist) != 1 || plist[0].Name != "phone2" {
		t.Fatalf("after rename: %+v", plist)
	}

	if code := c.Run([]string{"peer", "revoke", "phone2"}); code != 0 {
		t.Fatalf("revoke code = %d, stderr: %s", code, stderr.String())
	}
	plist, _ = peers.Load(filepath.Join(cfg, "peers.db"))
	if len(plist) != 0 {
		t.Fatalf("after revoke: %+v", plist)
	}
	// rename + revoke both apply conf: syncconf, restart awg, restart dns
	want := []string{"exec", "restart", "restart", "exec", "restart", "restart"}
	if strings.Join(rec.compose, ",") != strings.Join(want, ",") {
		t.Fatalf("compose calls = %v, want %v", rec.compose, want)
	}
}

func TestDoctorJSONExitCode(t *testing.T) {
	rootDir := t.TempDir()
	t.Setenv("AWG_MESH_ROOT", rootDir)
	var stdout, stderr bytes.Buffer
	c, _ := stubbedCli(t, &stdout, &stderr)
	c.DoctorRunChecks = func(root string) []doctor.Check {
		return []doctor.Check{
			{Name: "docker-daemon", OK: true, Detail: "docker daemon reachable"},
			{Name: "magicdns", OK: false, Detail: "hub.awg resolves to 100.64.0.1 via dnsmasq"},
		}
	}
	if code := c.Run([]string{"--json", "doctor"}); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	var out struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK || len(out.Checks) != 2 || out.Checks[0].Name != "docker-daemon" || out.Checks[1].OK {
		t.Fatalf("unexpected doctor JSON: %+v", out)
	}
}

func TestUnknownCommandExitCode2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c, _ := stubbedCli(t, &stdout, &stderr)
	if code := c.Run([]string{"nope"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
