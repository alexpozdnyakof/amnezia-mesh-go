// Package doctor runs post-install self-checks. Check names, order and
// retry behaviour match the Python reference (and the original bash
// implementation before it). Results are aggregated by the CLI and
// serialized as a single JSON document.
package doctor

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/compose"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/envfile"
)

// Retry behaviour for checks that need a warm-up after container start.
const (
	Retries    = 15
	RetryDelay = time.Second
)

// Check is one self-check result.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func docker(args ...string) compose.Result {
	var stdout, stderr strings.Builder
	cmd := exec.Command("docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return compose.Result{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// RunChecks returns the checks in a fixed order. After a failed
// "initialized" check only the first two checks are returned.
func RunChecks(root string) []Check {
	var checks []Check
	add := func(name string, ok bool, detail string) {
		checks = append(checks, Check{Name: name, OK: ok, Detail: detail})
	}
	envFile := filepath.Join(root, "config", "server.env")

	daemon := docker("info")
	add("docker-daemon", daemon.ExitCode == 0, "docker daemon reachable")

	initialized := fileExists(envFile)
	add("initialized", initialized, envFile)
	if !initialized {
		return checks
	}

	env, _ := envfile.Load(envFile)
	comp := func(args ...string) compose.Result {
		return compose.Run(root, env, "", args...)
	}

	// The userspace daemon needs a moment after container start. Note:
	// `awg show <iface> listen-port` misbehaves in amneziawg-tools (kernel
	// netlink fallback) — parse the full `awg show` output instead.
	upRe := map[string]*regexp.Regexp{
		"awg": regexp.MustCompile(`amnezia-mesh-awg.*Up`),
		"dns": regexp.MustCompile(`amnezia-mesh-dns.*Up`),
	}
	psDetail := func(service string, ok bool, stdout string) string {
		if !ok || stdout == "" {
			return ""
		}
		status := comp("ps", "--format", "{{.Status}}", service)
		return strings.Split(status.Stdout, "\n")[0]
	}
	ps := comp("ps", "--format", "{{.Name}} {{.Status}}")
	add("awg-container",
		ps.ExitCode == 0 && upRe["awg"].MatchString(ps.Stdout),
		psDetail("awg", ps.ExitCode == 0, ps.Stdout))
	psDNS := comp("ps", "--format", "{{.Name}} {{.Status}}")
	add("dns-container",
		psDNS.ExitCode == 0 && upRe["dns"].MatchString(psDNS.Stdout),
		psDetail("dns", psDNS.ExitCode == 0, psDNS.Stdout))

	listen := ""
	listenRe := regexp.MustCompile(`listening port:\s*(\d+)`)
	for range Retries {
		show := comp("exec", "-T", "awg", "awg", "show")
		if m := listenRe.FindStringSubmatch(show.Stdout); show.ExitCode == 0 && m != nil {
			listen = m[1]
			break
		}
		time.Sleep(RetryDelay)
	}
	add("listen-port", listen != "", "awg0 listens on "+listen)

	fwd := comp("exec", "-T", "awg", "cat", "/proc/sys/net/ipv4/ip_forward")
	add("ip-forward", fwd.ExitCode == 0 && strings.Contains(fwd.Stdout, "1"),
		"net.ipv4.ip_forward inside awg netns")

	domain := envfile.Get(env, "AWG_MESH_DOMAIN", "awg")
	p, err := netip.ParsePrefix(env["AWG_MESH_SUBNET"])
	hub := ""
	if err == nil {
		hub = p.Masked().Addr().Next().String()
	}
	// dnsmasq sidecar needs a moment (apk add + bind on awg0).
	dnsOK := false
	for range Retries {
		ns := comp("exec", "-T", "dns", "nslookup", "hub."+domain, hub)
		if ns.ExitCode == 0 && strings.Contains(ns.Stdout, hub) {
			dnsOK = true
			break
		}
		time.Sleep(RetryDelay)
	}
	add("magicdns", dnsOK, "hub."+domain+" resolves to "+hub+" via dnsmasq")

	port := env["AWG_MESH_PORT"]
	dp := docker("port", "amnezia-mesh-awg")
	add("port-published", dp.ExitCode == 0 && strings.Contains(dp.Stdout, ":"+port),
		"udp "+port+" -> 41480")

	return checks
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
