// Package cli implements the awg-mesh command surface: init / adopt /
// peer add|list|revoke|rename / doctor / monitoring, a global --json
// accepted anywhere on the command line, env overrides AWG_MESH_* and
// non-interactive mode via AWG_MESH_NONINTERACTIVE=1.
package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/adopt"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/compose"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/doctor"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/envfile"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/keygen"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/mesh"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/profile"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/render"
)

// Version mirrors awg_mesh.__version__. Overridden via -ldflags by goreleaser.
var Version = "0.2.0"

// errExit signals that a failure message was already printed.
var errExit = errors.New("cli: exit")

// Cli carries the I/O streams, JSON mode and the indirection points tests
// replace (the Go equivalent of the Python suite's monkeypatching).
type Cli struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	JSON           bool

	ComposeRun        func(root string, env map[string]string, stdin string, args ...string) compose.Result
	DoctorRunChecks   func(root string) []doctor.Check
	KeygenPubFromPriv func(image, priv string) (string, error)
	DockerRun         func(args ...string) (stdout, stderr string, exitCode int)
	Now               func() time.Time
}

// New returns a Cli wired to the real implementations.
func New(stdin io.Reader, stdout, stderr io.Writer) *Cli {
	return &Cli{
		Stdin:             stdin,
		Stdout:            stdout,
		Stderr:            stderr,
		ComposeRun:        compose.Run,
		DoctorRunChecks:   doctor.RunChecks,
		KeygenPubFromPriv: keygen.PubFromPriv,
		DockerRun:         runDocker,
		Now:               func() time.Time { return time.Now().UTC() },
	}
}

func runDocker(args ...string) (string, string, int) {
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return stdout.String(), stderr.String(), code
}

// root is the deployment root holding config/, docker-compose.yml, scripts/.
// Overridable via AWG_MESH_ROOT (tests and the parity harness point it at a
// tmpdir); default is the current directory.
func root() string {
	if r := os.Getenv("AWG_MESH_ROOT"); r != "" {
		return r
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return New(stdin, stdout, stderr).Run(args)
}

// Run executes args and returns the process exit code.
func (c *Cli) Run(args []string) int {
	argv := make([]string, 0, len(args))
	// The bash CLI accepted --json anywhere on the command line; keep that.
	for _, a := range args {
		if a == "--json" {
			c.JSON = true
			continue
		}
		argv = append(argv, a)
	}
	if len(argv) > 0 && (argv[0] == "--version" || argv[0] == "-version") {
		fmt.Fprintf(c.Stdout, "awg-mesh %s\n", Version)
		return 0
	}
	code, err := c.dispatch(argv)
	if err == nil {
		return code
	}
	if errors.Is(err, errExit) {
		return 1
	}
	var me *mesh.Error
	if errors.As(err, &me) {
		c.failf("%s", me.Msg)
		return 1
	}
	c.failf("%v", err)
	return 1
}

func (c *Cli) dispatch(argv []string) (int, error) {
	if len(argv) == 0 {
		usage(c.Stderr)
		return 2, nil
	}
	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "init":
		return c.cmdInit(rest)
	case "adopt":
		return c.cmdAdopt(rest)
	case "peer":
		return c.cmdPeer(rest)
	case "doctor":
		return c.cmdDoctor(rest)
	case "monitoring":
		return c.cmdMonitoring(rest)
	default:
		fmt.Fprintf(c.Stderr, "awg-mesh: error: unknown command: %s\n", cmd)
		usage(c.Stderr)
		return 2, nil
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: awg-mesh [--json] <command> [args]")
	fmt.Fprintln(w, "commands: init, adopt, peer add|list|revoke|rename, doctor, monitoring install|uninstall")
}

// say is a progress note, human mode only (stdout stays machine-readable).
func (c *Cli) say(format string, args ...any) {
	if !c.JSON {
		fmt.Fprintf(c.Stderr, format+"\n", args...)
	}
}

// out prints a line to stdout in human mode.
func (c *Cli) out(msg string) {
	if !c.JSON {
		fmt.Fprintln(c.Stdout, msg)
	}
}

// failf prints the failure message (json-aware) and returns errExit.
func (c *Cli) failf(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if c.JSON {
		o := ojson.New()
		o.Set("ok", false)
		o.Set("error", msg)
		var buf bytes.Buffer
		o.MarshalSpaced(&buf)
		fmt.Fprintln(c.Stdout, buf.String())
	} else {
		fmt.Fprintf(c.Stderr, "amnezia-mesh: error: %s\n", msg)
	}
	return errExit
}

// ok prints {"ok": true, ...fields} in JSON mode. Fields are k,v pairs and
// keep call order, like Python's {"ok": True, **fields}.
func (c *Cli) ok(pairs ...any) {
	if !c.JSON {
		return
	}
	o := ojson.New()
	o.Set("ok", true)
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	var buf bytes.Buffer
	o.MarshalSpaced(&buf)
	fmt.Fprintln(c.Stdout, buf.String())
}

// confirm asks y/N unless AWG_MESH_NONINTERACTIVE=1 (then always yes).
func (c *Cli) confirm(question string) bool {
	if os.Getenv("AWG_MESH_NONINTERACTIVE") == "1" {
		return true
	}
	fmt.Fprintf(c.Stderr, "%s [y/N] ", question)
	ans, err := bufio.NewReader(c.Stdin).ReadString('\n')
	if err != nil && len(ans) == 0 {
		return false
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

func (c *Cli) utcNow() string {
	return c.Now().Format("2006-01-02T15:04:05Z")
}

// --- init -------------------------------------------------------------------

func (c *Cli) cmdInit(args []string) (int, error) {
	flags, pos, err := parseArgs(args, map[string]bool{"rdp-reject": true})
	if err != nil || len(pos) != 0 {
		if err == nil {
			err = fmt.Errorf("unexpected positional arguments: %s", strings.Join(pos, " "))
		}
		return 2, c.argErr("init", err)
	}
	r := root()
	envFile := filepath.Join(r, "config", "server.env")
	if fileExists(envFile) {
		return 0, c.failf("already initialized: %s exists (delete config/ to start over)", envFile)
	}
	host := flags["host"]
	if host == "" {
		if os.Getenv("AWG_MESH_NONINTERACTIVE") == "1" {
			return 0, c.failf("--host is required in non-interactive mode")
		}
		fmt.Fprint(c.Stderr, "Public host or IP of this server: ")
		host = readLine(c.Stdin)
	}
	if host == "" {
		return 0, c.failf("host is required")
	}
	port := defaultStr(flags["port"], "443")
	if _, err := strconv.Atoi(port); err != nil {
		return 0, c.failf("invalid --port: %s", port)
	}
	subnet := defaultStr(flags["subnet"], "100.64.0.0/24")
	image := defaultStr(flags["image"], "amneziavpn/amneziawg-go:latest")
	domain := defaultStr(flags["domain"], "awg")

	c.say("pulling image %s ...", image)
	stdout, stderr, code := c.DockerRun("pull", image)
	_ = stdout
	if code != 0 {
		return 0, c.failf("docker pull failed: %s: %s", image, strings.TrimSpace(stderr))
	}

	c.say("probing image for I1-I5 (CPS) support ...")
	iSup := keygen.ProbeISupport(image)
	if iSup == "NO_AWGQUICK" {
		return 0, c.failf("image %s has no awg-quick; use the standard amneziavpn/amneziawg-go image", image)
	}

	c.say("generating server keys and obfuscation profile ...")
	serverPriv, serverPub, err := keygen.GenKeypair(image)
	if err != nil {
		return 0, c.failf("%v", err)
	}

	if err := os.MkdirAll(filepath.Join(r, "config", "peers"), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(r, "config", "peers.db"), os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	f.Close()
	lines := []string{
		"# amnezia-mesh server state. Local secrets — never commit.",
		"AWG_MESH_HOST=" + host,
		"AWG_MESH_PORT=" + port,
		"AWG_MESH_SUBNET=" + subnet,
		"AWG_MESH_DOMAIN=" + domain,
		"AWG_MESH_DNS1=1.1.1.1",
		"AWG_MESH_DNS2=1.0.0.1",
		"AWG_MESH_IMAGE=" + image,
		"AWG_MESH_RDP_REJECT=" + boolBit(flags["rdp-reject"] == "true"),
		"AWG_MESH_I_SUPPORTED=" + boolBit(iSup == "SUPPORTED"),
		"LISTEN_PORT=41480",
		"MTU=1280",
		"SERVER_KEEPALIVE=25",
		"CLIENT_KEEPALIVE=15-20",
		"SERVER_PRIV=" + serverPriv,
		"SERVER_PUB=" + serverPub,
	}
	lines = append(lines, keygen.GenObfuscation()...)
	if err := os.WriteFile(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return 0, err
	}
	if err := os.Chmod(envFile, 0o600); err != nil {
		return 0, err
	}

	if _, err := render.RenderAll(r); err != nil {
		return 0, err
	}

	c.say("starting containers ...")
	env, _ := envfile.Load(envFile)
	up := c.ComposeRun(r, env, "", "up", "-d")
	if up.ExitCode != 0 {
		return 0, c.failf("docker compose up failed: %s", strings.TrimSpace(up.Stderr))
	}

	results := c.DoctorRunChecks(r)
	for _, chk := range results {
		if !chk.OK {
			return 0, c.failf("post-start checks failed (see: awg-mesh doctor)")
		}
	}

	c.out("")
	c.out("amnezia-mesh is up.")
	c.out("Next: awg-mesh peer add <device-name>   # then import the printed vpn:// link in the Amnezia app")
	portNum, _ := strconv.Atoi(port)
	c.ok("host", host, "port", portNum, "subnet", subnet, "i_supported", iSup == "SUPPORTED")
	return 0, nil
}

// --- adopt ------------------------------------------------------------------

func (c *Cli) cmdAdopt(args []string) (int, error) {
	flags, pos, err := parseArgs(args, map[string]bool{"rdp-reject": true, "inspect": true})
	if err != nil || len(pos) != 0 {
		if err == nil {
			err = fmt.Errorf("unexpected positional arguments: %s", strings.Join(pos, " "))
		}
		return 2, c.argErr("adopt", err)
	}
	container, config, image := flags["container"], flags["config"], flags["image"]
	if (container == "") == (config == "") {
		return 2, c.argErr("adopt", fmt.Errorf("one of --container or --config is required"))
	}
	inspect := flags["inspect"] == "true"
	r := root()
	envFile := filepath.Join(r, "config", "server.env")
	if !inspect {
		if fileExists(envFile) {
			return 0, c.failf("already initialized: %s exists (delete config/ to start over)", envFile)
		}
		if flags["host"] == "" {
			return 0, c.failf("--host is required (public IP or domain of this server)")
		}
	}

	var confText, source string
	if container != "" {
		c.say("reading config from container '%s' ...", container)
		stdout, stderr, code := c.DockerRun("exec", container, "cat", "/opt/amnezia/awg/awg0.conf")
		if code != 0 {
			return 0, c.failf("cannot read awg0.conf from container '%s': %s",
				container, strings.TrimSpace(stderr))
		}
		confText = stdout
		inspOut, inspErr, code := c.DockerRun("inspect", "-f", "{{.Config.Image}}", container)
		if code != 0 || strings.TrimSpace(inspOut) == "" {
			return 0, c.failf("cannot inspect container '%s': %s",
				container, strings.TrimSpace(inspErr))
		}
		image = strings.TrimSpace(inspOut)
		source = fmt.Sprintf("container '%s'", container)
	} else {
		if image == "" {
			return 0, c.failf("--image is required with --config")
		}
		c.say("reading config from %s ...", config)
		data, err := os.ReadFile(config)
		if err != nil {
			return 0, c.failf("cannot read %s: %v", config, err)
		}
		confText = string(data)
		source = config
	}

	parsed, err := adopt.Parse(confText)
	if err != nil {
		return 0, err
	}

	c.say("deriving server public key ...")
	serverPub, err := c.KeygenPubFromPriv(image, parsed.Env["SERVER_PRIV"])
	if err != nil {
		return 0, c.failf("%v", err)
	}

	if inspect {
		if c.JSON {
			o := ojson.New()
			o.Set("ok", true)
			o.Set("inspect", inspectSummary(parsed, image, serverPub, source))
			var buf bytes.Buffer
			o.MarshalSpaced(&buf)
			fmt.Fprintln(c.Stdout, buf.String())
		} else {
			c.sayAdoptPlan(parsed)
		}
		return 0, nil
	}

	c.sayAdoptPlan(parsed)
	host := flags["host"]
	port, err := strconv.Atoi(defaultStr(flags["port"], "443"))
	if err != nil {
		return 0, c.failf("invalid --port: %s", flags["port"])
	}
	envPairs, records := adopt.BuildState(
		parsed, host, port, defaultStr(flags["domain"], "awg"), image,
		defaultStr(flags["dns1"], "1.1.1.1"), flags["dns2"],
		flags["rdp-reject"] == "true", serverPub, c.utcNow())

	if err := os.MkdirAll(filepath.Join(r, "config", "peers"), 0o755); err != nil {
		return 0, err
	}
	lines := []string{
		fmt.Sprintf("# amnezia-mesh server state. Adopted from %s. Local secrets — never commit.", source),
	}
	for _, kv := range envPairs {
		lines = append(lines, kv[0]+"="+kv[1])
	}
	if err := os.WriteFile(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return 0, err
	}
	if err := os.Chmod(envFile, 0o600); err != nil {
		return 0, err
	}
	if err := peers.WithLock(filepath.Join(r, "config", "peers.db"), func(tx *peers.Tx) error {
		for _, rec := range records {
			tx.Add(rec)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	c.say("wrote config/server.env (0600) and config/peers.db (%d peers)", len(records))

	c.say("rendering awg0.conf / dnsmasq.conf / start.sh ...")
	if _, err := render.RenderAll(r); err != nil {
		return 0, err
	}

	old := container
	if old != "" {
		c.say("stopping '%s' (kept stopped for rollback — do not remove) ...", old)
		_, stderr, code := c.DockerRun("stop", old)
		if code != 0 {
			return 0, c.failf("old container refused to stop: %s (config written, nothing else changed — investigate manually)",
				strings.TrimSpace(stderr))
		}
	}
	c.say("starting amnezia-mesh stack (awg + dns) ...")
	env, _ := envfile.Load(envFile)
	up := c.ComposeRun(r, env, "", "up", "-d")
	if up.ExitCode != 0 {
		if old != "" {
			c.DockerRun("start", old)
			return 0, c.failf("docker compose up failed: %s; '%s' was started back, nothing deleted",
				strings.TrimSpace(up.Stderr), old)
		}
		return 0, c.failf("docker compose up failed: %s", strings.TrimSpace(up.Stderr))
	}

	results := c.DoctorRunChecks(r)
	var bad []doctor.Check
	for _, chk := range results {
		if !chk.OK {
			bad = append(bad, chk)
		}
	}
	if len(bad) > 0 {
		if old != "" {
			c.ComposeRun(r, env, "", "down")
			c.DockerRun("start", old)
		}
		parts := make([]string, 0, len(bad))
		for _, chk := range bad {
			parts = append(parts, chk.Name+": "+chk.Detail)
		}
		detail := strings.Join(parts, "; ")
		if old != "" {
			return 0, c.failf("post-adopt checks failed: %s — rolled back, '%s' is running again, nothing deleted",
				detail, old)
		}
		return 0, c.failf("post-adopt checks failed: %s", detail)
	}

	c.out("")
	c.out(fmt.Sprintf("adopt complete: %d peers carried over, IPs and keys unchanged", len(records)))
	c.out("client profiles keep working — no reissue needed")
	if old != "" {
		c.out(fmt.Sprintf("rollback anytime: docker stop amnezia-mesh-awg amnezia-mesh-dns && docker start %s", old))
	}
	c.out("next: check a client reconnects, then ./bin/awg-mesh peer list")
	adoptedFrom := config
	if container != "" {
		adoptedFrom = container
	}
	c.ok("adopted_from", adoptedFrom, "peers", len(records), "subnet", parsed.Subnet)
	return 0, nil
}

// --- peer -------------------------------------------------------------------

// inspectSummary is the machine-readable pre-adopt snapshot for
// `adopt --inspect`. Key order matches the Python dict.
func inspectSummary(parsed *adopt.ParsedConf, image, serverPub, source string) *ojson.Obj {
	peers := make([]any, 0, len(parsed.Peers))
	for _, p := range parsed.Peers {
		o := ojson.New()
		o.Set("name", p.Name)
		o.Set("ip", p.IP)
		o.Set("pub", p.Pub)
		o.Set("created", p.Created)
		peers = append(peers, o)
	}
	warnings := make([]any, 0, len(parsed.Warnings))
	for _, w := range parsed.Warnings {
		warnings = append(warnings, w)
	}
	o := ojson.New()
	o.Set("source", source)
	o.Set("image", image)
	o.Set("server_pub", serverPub)
	o.Set("subnet", parsed.Subnet)
	o.Set("mtu", parsed.Env["MTU"]) // absent → "", like Python's .get(k, "")
	o.Set("listen_port", parsed.Env["LISTEN_PORT"])
	o.Set("server_keepalive", parsed.Keepalive)
	o.Set("obfuscation_params", obfCount(parsed))
	o.Set("peers", peers)
	o.Set("warnings", warnings)
	return o
}

func obfCount(parsed *adopt.ParsedConf) int {
	n := 0
	for k := range parsed.Env {
		if strings.HasPrefix(k, "OBF_") {
			n++
		}
	}
	return n
}

// sayAdoptPlan prints the human-mode adopt plan (progress notes, stderr).
func (c *Cli) sayAdoptPlan(parsed *adopt.ParsedConf) {
	c.say("[Interface]: server key, %d obfuscation parameters, MTU %s, ListenPort %s",
		obfCount(parsed), defaultStr(parsed.Env["MTU"], "1280"),
		defaultStr(parsed.Env["LISTEN_PORT"], "41480"))
	c.say("[Peer] x%d:", len(parsed.Peers))
	for _, p := range parsed.Peers {
		c.say("  %-20s %s", p.Name, p.IP)
	}
	for _, w := range parsed.Warnings {
		c.say("warning: %s", w)
	}
}

func (c *Cli) cmdPeer(args []string) (int, error) {
	if len(args) == 0 {
		usage(c.Stderr)
		return 2, nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return c.cmdPeerAdd(rest)
	case "list":
		return c.cmdPeerList(rest)
	case "revoke":
		return c.cmdPeerRevoke(rest)
	case "rename":
		return c.cmdPeerRename(rest)
	default:
		fmt.Fprintf(c.Stderr, "awg-mesh: error: unknown peer command: %s\n", sub)
		usage(c.Stderr)
		return 2, nil
	}
}

func (c *Cli) requireInit(r string) (string, error) {
	envFile := filepath.Join(r, "config", "server.env")
	if !fileExists(envFile) {
		return "", c.failf("not initialized (run: awg-mesh init)")
	}
	return envFile, nil
}

func (c *Cli) cmdPeerAdd(args []string) (int, error) {
	flags, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = fmt.Errorf("expected exactly one positional argument: name")
		}
		return 2, c.argErr("peer add", err)
	}
	name := pos[0]
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	if !peers.ValidName(name) {
		return 0, c.failf("invalid peer name: %s (lowercase letters, digits, dash, max 32)", name)
	}
	ptype := defaultStr(flags["type"], "both")
	if ptype != "exit" && ptype != "mesh" && ptype != "both" {
		return 0, c.failf("--type must be exit|mesh|both")
	}
	env, err := envfile.Load(envFile)
	if err != nil {
		return 0, err
	}

	// The lock is held for the whole check-generate-append window: two
	// concurrent `peer add` runs serialize instead of racing on the same IP.
	var priv, pub, psk, ip, created string
	err = peers.WithLock(filepath.Join(r, "config", "peers.db"), func(tx *peers.Tx) error {
		if tx.Find(name) != nil {
			return c.failf("peer already exists: %s", name)
		}
		ipAddr, ok, err := peers.NextFreeIP(env["AWG_MESH_SUBNET"], tx.Peers)
		if err != nil {
			return err
		}
		if !ok {
			return c.failf("subnet %s exhausted", env["AWG_MESH_SUBNET"])
		}
		c.say("generating keys for %s ...", name)
		priv, pub, err = keygen.GenKeypair(env["AWG_MESH_IMAGE"])
		if err != nil {
			return c.failf("%v", err)
		}
		psk, err = keygen.GenPSK(env["AWG_MESH_IMAGE"])
		if err != nil {
			return c.failf("%v", err)
		}
		ip, created = ipAddr, c.utcNow()
		tx.Add(peers.Peer{Name: name, IP: ip, Pub: pub, PSK: psk, Created: created})
		return nil
	})
	if err != nil {
		return 0, err
	}

	if _, err := render.RenderAll(r); err != nil {
		return 0, err
	}
	if err := c.applyConf(r, env); err != nil {
		return 0, err
	}

	prefix := filepath.Join(r, "config", "peers", name)
	if _, err := profile.GenerateProfiles(envFile, name, ip, priv, pub, psk, prefix); err != nil {
		return 0, err
	}

	c.out("")
	c.out(fmt.Sprintf("peer '%s' added: %s (profiles: %s-exit.vpn / %s-mesh.vpn)", name, ip, prefix, prefix))
	c.out("import the .vpn link in the Amnezia app, or use the .conf with any awg/wg client")
	c.ok("name", name, "ip", ip,
		"profiles", []any{prefix + "-exit.vpn", prefix + "-mesh.vpn"})
	return 0, nil
}

func (c *Cli) cmdPeerList(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 0 {
		return 2, c.argErr("peer list", err)
	}
	r := root()
	if _, err := c.requireInit(r); err != nil {
		return 0, err
	}
	plist, err := peers.Load(filepath.Join(r, "config", "peers.db"))
	if err != nil {
		return 0, err
	}
	if !c.JSON {
		if len(plist) == 0 {
			c.out("no peers")
		} else {
			for _, p := range plist {
				c.out(fmt.Sprintf("%-20s %-15s %s", p.Name, p.IP, p.Created))
			}
		}
	}
	peerObjs := make([]any, 0, len(plist))
	for _, p := range plist {
		o := ojson.New()
		o.Set("name", p.Name)
		o.Set("ip", p.IP)
		o.Set("pub", p.Pub)
		o.Set("created", p.Created)
		peerObjs = append(peerObjs, o)
	}
	c.ok("peers", peerObjs)
	return 0, nil
}

func (c *Cli) cmdPeerRename(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = fmt.Errorf("expected: peer rename <name> <new_name>")
		}
		return 2, c.argErr("peer rename", err)
	}
	oldName, newName := pos[0], pos[1]
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	if !peers.ValidName(newName) {
		return 0, c.failf("invalid peer name: %s (lowercase letters, digits, dash, max 32)", newName)
	}
	env, err := envfile.Load(envFile)
	if err != nil {
		return 0, err
	}
	err = peers.WithLock(filepath.Join(r, "config", "peers.db"), func(tx *peers.Tx) error {
		if tx.Find(oldName) == nil {
			return c.failf("no such peer: %s", oldName)
		}
		if !tx.Rename(oldName, newName) {
			return c.failf("peer already exists: %s", newName)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if _, err := render.RenderAll(r); err != nil {
		return 0, err
	}
	if err := c.applyConf(r, env); err != nil {
		return 0, err
	}
	c.say("peer '%s' renamed to '%s'", oldName, newName)
	c.ok("renamed", oldName, "name", newName)
	return 0, nil
}

func (c *Cli) cmdPeerRevoke(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = fmt.Errorf("expected exactly one positional argument: name")
		}
		return 2, c.argErr("peer revoke", err)
	}
	name := pos[0]
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	env, err := envfile.Load(envFile)
	if err != nil {
		return 0, err
	}
	err = peers.WithLock(filepath.Join(r, "config", "peers.db"), func(tx *peers.Tx) error {
		if tx.Find(name) == nil {
			return c.failf("no such peer: %s", name)
		}
		if !c.confirm(fmt.Sprintf("revoke peer '%s'? its devices will lose access", name)) {
			return c.failf("aborted")
		}
		tx.Remove(name)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if _, err := render.RenderAll(r); err != nil {
		return 0, err
	}
	if err := c.applyConf(r, env); err != nil {
		return 0, err
	}
	matches, _ := filepath.Glob(filepath.Join(r, "config", "peers", name+"-*.vpn"))
	confMatches, _ := filepath.Glob(filepath.Join(r, "config", "peers", name+"-*.conf"))
	for _, path := range append(matches, confMatches...) {
		os.Remove(path)
	}
	c.say("peer '%s' revoked", name)
	c.ok("revoked", name)
	return 0, nil
}

// --- doctor / monitoring ----------------------------------------------------

func (c *Cli) cmdDoctor(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 0 {
		return 2, c.argErr("doctor", err)
	}
	results := c.DoctorRunChecks(root())
	allOK := true
	for _, chk := range results {
		if !chk.OK {
			allOK = false
		}
	}
	if c.JSON {
		o := ojson.New()
		o.Set("ok", allOK)
		checks := make([]any, 0, len(results))
		for _, chk := range results {
			co := ojson.New()
			co.Set("name", chk.Name)
			co.Set("ok", chk.OK)
			co.Set("detail", chk.Detail)
			checks = append(checks, co)
		}
		o.Set("checks", checks)
		var buf bytes.Buffer
		o.MarshalSpaced(&buf)
		fmt.Fprintln(c.Stdout, buf.String())
	} else {
		for _, chk := range results {
			mark := "ok  "
			if !chk.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(c.Stdout, "%s  %-18s %s\n", mark, chk.Name, chk.Detail)
		}
	}
	if allOK {
		return 0, nil
	}
	return 1, nil
}

func (c *Cli) cmdMonitoring(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 || (pos[0] != "install" && pos[0] != "uninstall") {
		if err == nil {
			err = fmt.Errorf("action must be install|uninstall")
		}
		return 2, c.argErr("monitoring", err)
	}
	r := root()
	scripts := filepath.Join(r, "scripts")
	if pos[0] == "install" {
		steps := [][]string{
			{"sudo", "cp",
				filepath.Join(scripts, "awg-stats.sh"),
				filepath.Join(scripts, "awg-watchdog.sh"),
				"/usr/local/sbin/"},
			{"sudo", "cp",
				filepath.Join(scripts, "amnezia-mesh.cron"),
				"/etc/cron.d/amnezia-mesh"},
		}
		for _, step := range steps {
			if _, stderr, code := runCmd(step[0], step[1:]...); code != 0 {
				return 0, c.failf("install failed (sudo?): %s", strings.TrimSpace(stderr))
			}
		}
		c.say("monitoring installed: /usr/local/sbin/awg-{stats,watchdog}.sh + /etc/cron.d/amnezia-mesh")
		c.ok("installed", true)
		return 0, nil
	}
	_, stderr, code := runCmd("sudo", "rm", "-f",
		"/usr/local/sbin/awg-stats.sh", "/usr/local/sbin/awg-watchdog.sh",
		"/etc/cron.d/amnezia-mesh")
	if code != 0 {
		return 0, c.failf("uninstall failed (sudo?): %s", strings.TrimSpace(stderr))
	}
	c.say("monitoring removed")
	c.ok("installed", false)
	return 0, nil
}

func runCmd(name string, args ...string) (string, string, int) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return stdout.String(), stderr.String(), code
}

// --- apply ------------------------------------------------------------------

// applyConf pushes awg0.conf into the running container and reloads dns.
func (c *Cli) applyConf(r string, env map[string]string) error {
	data, err := os.ReadFile(filepath.Join(r, "config", "awg0.conf"))
	if err != nil {
		return err
	}
	peersOnly := StripInterfaceBlock(string(data))
	sync := c.ComposeRun(r, env, peersOnly+"\n",
		"exec", "-T", "awg", "awg", "syncconf", "awg0", "/dev/stdin")
	if sync.ExitCode == 0 {
		c.say("applied via awg syncconf")
	} else {
		c.say("syncconf failed (%s)", strings.TrimSpace(sync.Stderr))
	}
	// Production lesson: the userspace daemon can silently wedge fresh
	// handshakes after peers are stripped/re-added via syncconf (the iphone
	// incident). Restart unconditionally — syncconf alone gives false success.
	c.ComposeRun(r, env, "", "restart", "awg")
	// dnsmasq ignores SIGHUP for config changes — restart the sidecar too.
	c.ComposeRun(r, env, "", "restart", "dns")
	return nil
}

// StripInterfaceBlock removes the [Interface] section from an awg-quick
// config. syncconf only understands [Peer] sections ([Interface] keys like
// Address are wg-quick-isms and rejected), so we strip the block before
// piping the config in.
func StripInterfaceBlock(text string) string {
	var lines []string
	skipping := false
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "[Interface]" {
			skipping = true
			continue
		}
		if skipping && strings.HasPrefix(line, "[") {
			skipping = false
		}
		if !skipping {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// --- tiny arg parser --------------------------------------------------------

// parseArgs splits args into --flag values (and --flag=value) and
// positionals. Flags listed in bools take no value.
func parseArgs(args []string, bools map[string]bool) (map[string]string, []string, error) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		name, value, hasValue := strings.Cut(a[2:], "=")
		if bools[name] {
			if hasValue {
				flags[name] = value
			} else {
				flags[name] = "true"
			}
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("argument --%s: expected one argument", name)
			}
			i++
			value = args[i]
		}
		flags[name] = value
	}
	return flags, pos, nil
}

func (c *Cli) argErr(cmd string, err error) error {
	if err == nil {
		return nil
	}
	fmt.Fprintf(c.Stderr, "awg-mesh %s: error: %v\n", cmd, err)
	return errExit
}

func readLine(r io.Reader) string {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func boolBit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
