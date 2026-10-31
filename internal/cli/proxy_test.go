package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/skill"
)

// installScript writes an executable shim into a temp bin dir and prepends
// it to PATH (restored by t.Setenv).
func installScript(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

// fakeTransports installs ssh/scp shims: ssh logs its args and prints a
// marker; scp logs and mirrors $CANNED_PEERS into the destination as peers/.
func fakeTransports(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.log")
	canned := t.TempDir()
	if err := os.WriteFile(filepath.Join(canned, "phone-exit.conf"),
		[]byte("[Interface]\n# canned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANNED_PEERS", canned)
	installScript(t, "ssh",
		"printf '%s\\n' \"ssh $*\" >>\"$CALLS_LOG\"\n"+
			"echo \"FAKE-SSH-OUT\"\n"+
			"exit \"${FAKE_SSH_RC:-0}\"\n")
	installScript(t, "scp",
		"printf '%s\\n' \"scp $*\" >>\"$CALLS_LOG\"\n"+
			"dest=\"${!#}\"\n"+
			"if [ \"${FAKE_SCP_FAIL:-0}\" = 1 ]; then exit 1; fi\n"+
			"mkdir -p \"$dest\"\n"+
			"cp -R \"$CANNED_PEERS\" \"$dest/peers\"\n")
	t.Setenv("CALLS_LOG", log)
	t.Setenv("FAKE_SSH_RC", "0")
	t.Setenv("FAKE_SCP_FAIL", "0")
	return log
}

func callLog(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AWG_MESH_SSH", "AWG_MESH_REMOTE_ROOT", "AWG_MESH_NONINTERACTIVE",
	} {
		t.Setenv(k, "")
	}
}

func TestResolveRemote(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		env     string
		alias   string
		rest    []string
		wantErr bool
	}{
		{"separate", []string{"--server", "vps", "peer", "list"}, "", "vps", []string{"peer", "list"}, false},
		{"equals", []string{"--server=vps", "peer", "list"}, "", "vps", []string{"peer", "list"}, false},
		{"last wins, flag anywhere", []string{"peer", "--server", "one", "list", "--server=two"}, "", "two", []string{"peer", "list"}, false},
		{"env fallback", []string{"peer", "list"}, "env-vps", "env-vps", []string{"peer", "list"}, false},
		{"no alias", []string{"peer", "list"}, "", "", []string{"peer", "list"}, false},
		{"missing value", []string{"peer", "list", "--server"}, "", "", nil, true},
		{"empty equals", []string{"--server=", "peer", "list"}, "", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AWG_MESH_SSH", tc.env)
			alias, rest, err := resolveRemote(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got alias=%q", alias)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if alias != tc.alias || strings.Join(rest, " ") != strings.Join(tc.rest, " ") {
				t.Fatalf("alias=%q rest=%v, want alias=%q rest=%v",
					alias, rest, tc.alias, tc.rest)
			}
		})
	}
}

func TestIsLocalOnlyAndMutating(t *testing.T) {
	if !isLocalOnly([]string{"skill"}) || !isLocalOnly([]string{"--version"}) {
		t.Fatal("skill/--version must stay local")
	}
	if isLocalOnly([]string{"peer", "list"}) || isLocalOnly(nil) {
		t.Fatal("peer list / empty argv must not be local-only")
	}
	for _, argv := range [][]string{
		{"init"}, {"adopt", "--config", "/x"}, {"peer", "add", "p"},
		{"peer", "rename", "a", "b"}, {"peer", "revoke", "a"},
	} {
		if !isMutating(argv) {
			t.Fatalf("isMutating(%v) = false", argv)
		}
	}
	for _, argv := range [][]string{
		{"adopt", "--inspect", "--config", "/x"}, {"peer", "list"},
		{"doctor"}, {}, {"monitoring", "install"},
	} {
		if isMutating(argv) {
			t.Fatalf("isMutating(%v) = true", argv)
		}
	}
}

func TestShquote(t *testing.T) {
	cases := map[string]string{
		"peer":        "peer",
		"weird name!": "'weird name!'",
		"it's":        `'it'"'"'s'`,
		"":            "''",
		"~/x-y.z":     "'~/x-y.z'", // '~' is not in shlex's safe set
	}
	for in, want := range cases {
		if got := shquote(in); got != want {
			t.Fatalf("shquote(%q) = %q, want %q", in, got, want)
		}
	}
}

func remoteCmd(t *testing.T, argv []string, jsonMode bool) string {
	t.Helper()
	clearProxyEnv(t)
	c := New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	c.JSON = jsonMode
	return c.remoteCommand(argv)
}

func TestRemoteCommand(t *testing.T) {
	if got, want := remoteCmd(t, []string{"peer", "list"}, false),
		"cd ~ && awg-mesh peer list"; got != want {
		t.Fatalf("defaults: got %q want %q", got, want)
	}
	t.Setenv("AWG_MESH_REMOTE_ROOT", "/opt/mesh")
	t.Setenv("AWG_MESH_NONINTERACTIVE", "1")
	c := New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	c.JSON = true
	if got, want := c.remoteCommand([]string{"peer", "add", "phone"}),
		"cd /opt/mesh && AWG_MESH_NONINTERACTIVE=1 awg-mesh --json peer add phone"; got != want {
		t.Fatalf("env+json: got %q want %q", got, want)
	}
	t.Setenv("AWG_MESH_REMOTE_ROOT", "/opt/my mesh")
	if got, want := c.remoteCommand([]string{"doctor"}),
		`cd '/opt/my mesh' && AWG_MESH_NONINTERACTIVE=1 awg-mesh --json doctor`; got != want {
		t.Fatalf("spaced root: got %q want %q", got, want)
	}
}

func TestRunRemotePassesExitCode(t *testing.T) {
	log := fakeTransports(t)
	clearProxyEnv(t)
	t.Setenv("AWG_MESH_ROOT", t.TempDir())
	t.Setenv("FAKE_SSH_RC", "23")
	var stdout, stderr bytes.Buffer
	c := New(strings.NewReader(""), &stdout, &stderr)
	code, err := c.runRemote("vps", []string{"peer", "list"})
	if err != nil || code != 23 {
		t.Fatalf("code=%d err=%v, want 23", code, err)
	}
	if len(callLog(t, log)) != 1 {
		t.Fatalf("scp must not run: %v", callLog(t, log))
	}
}

func TestRunRemoteSyncsAfterMutation(t *testing.T) {
	log := fakeTransports(t)
	clearProxyEnv(t)
	rootDir := t.TempDir()
	t.Setenv("AWG_MESH_ROOT", rootDir)
	cfg := filepath.Join(rootDir, "config", "peers")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "stale.conf"),
		[]byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	c := New(strings.NewReader(""), &stdout, &stderr)
	c.JSON = true
	code, err := c.runRemote("vps", []string{"peer", "add", "phone"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v, stderr: %s", code, err, stderr.String())
	}
	pulled := filepath.Join(cfg, "phone-exit.conf")
	if _, err := os.Stat(pulled); err != nil {
		t.Fatalf("pulled profile missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "stale.conf")); !os.IsNotExist(err) {
		t.Fatal("stale local profile must be replaced by the remote mirror")
	}
	calls := callLog(t, log)
	if len(calls) != 2 ||
		calls[0] != "ssh vps cd ~ && awg-mesh --json peer add phone" ||
		!strings.HasPrefix(calls[1], "scp -r -q vps:~/config/peers ") {
		t.Fatalf("calls: %v", calls)
	}
}

func TestRunRemoteNoSyncOnFailure(t *testing.T) {
	log := fakeTransports(t)
	clearProxyEnv(t)
	t.Setenv("AWG_MESH_ROOT", t.TempDir())
	t.Setenv("FAKE_SSH_RC", "1")
	var stdout, stderr bytes.Buffer
	c := New(strings.NewReader(""), &stdout, &stderr)
	code, err := c.runRemote("vps", []string{"peer", "add", "phone"})
	if err != nil || code != 1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if len(callLog(t, log)) != 1 {
		t.Fatal("scp must not run after a failed remote command")
	}
}

func TestRunRemoteWarnsWhenScpFails(t *testing.T) {
	fakeTransports(t)
	clearProxyEnv(t)
	t.Setenv("AWG_MESH_ROOT", t.TempDir())
	t.Setenv("FAKE_SCP_FAIL", "1")
	var stdout, stderr bytes.Buffer
	c := New(strings.NewReader(""), &stdout, &stderr)
	code, err := c.runRemote("vps", []string{"init"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	want := "amnezia-mesh: warning: peer profiles not synced from 'vps' (scp failed)"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr %q missing %q", stderr.String(), want)
	}
}

func TestSkillAndVersionStayLocalWithServer(t *testing.T) {
	clearProxyEnv(t)
	fakeTransports(t)
	t.Setenv("AWG_MESH_ROOT", t.TempDir())
	var stdout, stderr bytes.Buffer
	c := New(strings.NewReader(""), &stdout, &stderr)
	if code := c.Run([]string{"--server", "vps", "skill"}); code != 0 {
		t.Fatalf("skill: code=%d", code)
	}
	if stdout.String() != skill.Load() {
		t.Fatal("skill must print the local bundled SKILL.md")
	}
	stdout.Reset()
	if code := c.Run([]string{"--server", "vps", "--version"}); code != 0 {
		t.Fatalf("version: code=%d", code)
	}
	if stdout.String() != "awg-mesh "+Version+"\n" {
		t.Fatalf("version output %q", stdout.String())
	}
}
