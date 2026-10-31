// Remote mode (stage 7): thin SSH proxy, byte-parity with the Python
// awg_mesh/proxy.py. --server <alias> / AWG_MESH_SSH runs
// `ssh <alias> 'cd <root> && awg-mesh <args>'` with the system ssh client
// and passes stdout/stderr/exit code through; after a mutating command
// (init, adopt without --inspect, peer add|rename|revoke) exits 0,
// config/peers/ is mirrored back with scp. skill and --version stay local.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveRemote splits --server out of argv. Last flag wins; without the
// flag AWG_MESH_SSH is the fallback. Empty alias means local mode.
func resolveRemote(argv []string) (alias string, rest []string, err error) {
	rest = make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--server":
			if i+1 >= len(argv) {
				return "", nil, fmt.Errorf("--server requires a value")
			}
			alias = argv[i+1]
			i++
		case strings.HasPrefix(a, "--server="):
			alias = strings.TrimPrefix(a, "--server=")
			if alias == "" {
				return "", nil, fmt.Errorf("--server requires a value")
			}
		default:
			rest = append(rest, a)
		}
	}
	if alias == "" {
		alias = os.Getenv("AWG_MESH_SSH")
	}
	return alias, rest, nil
}

// isLocalOnly reports commands that never go through the proxy.
func isLocalOnly(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "skill", "--version", "-version":
		return true
	}
	return false
}

// isMutating reports whether a successful run changes remote config/peers/.
func isMutating(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "init":
		return true
	case "adopt":
		for _, a := range argv[1:] {
			if a == "--inspect" {
				return false
			}
		}
		return true
	case "peer":
		if len(argv) > 1 {
			switch argv[1] {
			case "add", "rename", "revoke":
				return true
			}
		}
	}
	return false
}

// shquote mirrors Python shlex.quote (the parity contract for the remote
// command string): ASCII word chars plus _@%+=:,./- stay bare, "'" becomes
// '"'"”-escaped inside single quotes, empty string becomes ”.
func shquote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune(
			"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-", r)
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// remoteCommand builds the exact string the ssh client receives. The remote
// root "~" is passed bare — a quoted '~' would not expand on the remote side.
func (c *Cli) remoteCommand(argv []string) string {
	remoteRoot := os.Getenv("AWG_MESH_REMOTE_ROOT")
	if remoteRoot == "" {
		remoteRoot = "~"
	}
	cd := shquote(remoteRoot)
	if remoteRoot == "~" {
		cd = "~"
	}
	var sb strings.Builder
	sb.WriteString("cd ")
	sb.WriteString(cd)
	sb.WriteString(" && ")
	if os.Getenv("AWG_MESH_NONINTERACTIVE") == "1" {
		sb.WriteString("AWG_MESH_NONINTERACTIVE=1 ")
	}
	sb.WriteString("awg-mesh")
	if c.JSON {
		sb.WriteString(" --json")
	}
	for _, a := range argv {
		sb.WriteString(" ")
		sb.WriteString(shquote(a))
	}
	return sb.String()
}

// runRemote executes argv on the remote host and returns the ssh exit code.
func (c *Cli) runRemote(alias string, argv []string) (int, error) {
	cmd := exec.Command("ssh", alias, c.remoteCommand(argv))
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		return 1, c.failf("%s", "cannot run ssh: no system ssh client in $PATH")
	}
	if code == 0 && isMutating(argv) {
		c.syncPeers(alias)
	}
	return code, nil
}

// syncPeers mirrors <remote root>/config/peers into the local root after a
// mutation. Failure is a warning only — the remote change already happened.
func (c *Cli) syncPeers(alias string) {
	remoteRoot := os.Getenv("AWG_MESH_REMOTE_ROOT")
	if remoteRoot == "" {
		remoteRoot = "~"
	}
	cfg := filepath.Join(root(), "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		fmt.Fprintf(c.Stderr, "%s\n", syncWarning(alias))
		return
	}
	os.RemoveAll(filepath.Join(cfg, "peers"))
	cmd := exec.Command("scp", "-r", "-q",
		alias+":"+remoteRoot+"/config/peers", cfg)
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	if err := cmd.Run(); err != nil {
		// Non-zero exit or launch failure — same warning either way.
		fmt.Fprintf(c.Stderr, "%s\n", syncWarning(alias))
	}
}

func syncWarning(alias string) string {
	return fmt.Sprintf(
		"amnezia-mesh: warning: peer profiles not synced from '%s' (scp failed)",
		alias)
}
