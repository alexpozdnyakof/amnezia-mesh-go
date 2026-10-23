// Package compose is a docker compose wrapper. server.env values are passed
// as the compose process environment, mirroring the old
// `set -a; . server.env; docker compose ...` dance.
package compose

import (
	"os"
	"os/exec"
	"strings"
)

// Result mirrors subprocess.CompletedProcess as used by the Python CLI.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Run executes `docker compose args` in root with the host environment
// overlaid by env (env wins on duplicate keys, like dict(os.environ).update).
func Run(root string, env map[string]string, stdin string, args ...string) Result {
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = root
	cmd.Env = mergeEnv(os.Environ(), env)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return Result{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

func mergeEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]int, len(base))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if v, ok := overrides[k]; ok {
			kv = k + "=" + v
		}
		seen[k] = len(out)
		out = append(out, kv)
	}
	for k, v := range overrides {
		if _, ok := seen[k]; !ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
