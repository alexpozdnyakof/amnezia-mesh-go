// Custom MagicDNS records on top of <name>.<domain>: `awg-mesh dns
// add|remove|list`. Source of truth is config/custom.hosts (hosts format),
// the second dnsmasq --addn-hosts input; dnsmasq re-reads it on SIGHUP, so
// changes apply without restarting the sidecar. Byte-parity with the
// Python awg_mesh/cli.py dns commands.
package cli

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/envfile"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/render"
)

type customRecord struct {
	Name string // fqdn, e.g. nas.awg
	IP   string
}

func customHostsPath(r string) string {
	return filepath.Join(r, "config", "custom.hosts")
}

// loadCustomRecords reads config/custom.hosts; a missing file yields no
// records (not an error).
func loadCustomRecords(r string) ([]customRecord, error) {
	data, err := os.ReadFile(customHostsPath(r))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []customRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			records = append(records, customRecord{Name: fields[1], IP: fields[0]})
		}
	}
	return records, nil
}

// saveCustomRecords rewrites config/custom.hosts sorted by fqdn (Python's
// sorted() on (name, ip) tuples).
func saveCustomRecords(r string, records []customRecord) error {
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].IP < records[j].IP
	})
	var b strings.Builder
	b.WriteString(render.CustomHostsHeader)
	for _, rec := range records {
		fmt.Fprintf(&b, "%s\t%s\n", rec.IP, rec.Name)
	}
	if err := os.MkdirAll(filepath.Join(r, "config"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(customHostsPath(r), []byte(b.String()), 0o644)
}

// dnsDomain mirrors Python's env.get("AWG_MESH_DOMAIN", "awg"): the default
// applies only when the key is absent.
func dnsDomain(env map[string]string) string {
	if v, ok := env["AWG_MESH_DOMAIN"]; ok {
		return v
	}
	return "awg"
}

func (c *Cli) cmdDns(args []string) (int, error) {
	if len(args) == 0 {
		usage(c.Stderr)
		return 2, nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return c.cmdDnsAdd(rest)
	case "remove":
		return c.cmdDnsRemove(rest)
	case "list":
		return c.cmdDnsList(rest)
	default:
		fmt.Fprintf(c.Stderr, "awg-mesh: error: unknown dns command: %s\n", sub)
		usage(c.Stderr)
		return 2, nil
	}
}

func (c *Cli) cmdDnsAdd(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = fmt.Errorf("expected: dns add <name> <ip>")
		}
		return 2, c.argErr("dns add", err)
	}
	name, ipArg := pos[0], pos[1]
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	if strings.Contains(name, ".") || !peers.ValidName(name) {
		return 0, c.failf("invalid record name: %s (lowercase letters, digits, dash, max 32, no dots)", name)
	}
	addr, err := netip.ParseAddr(ipArg)
	if err != nil {
		return 0, c.failf("invalid IP: %s", ipArg)
	}
	if !addr.Is4() {
		return 0, c.failf("invalid IP: %s (IPv4 only)", ipArg)
	}
	env, err := envfile.Load(envFile)
	if err != nil {
		return 0, err
	}
	fqdn := name + "." + dnsDomain(env)
	records, err := loadCustomRecords(r)
	if err != nil {
		return 0, err
	}
	for _, rec := range records {
		if rec.Name == fqdn {
			return 0, c.failf("record already exists: %s (remove it first)", fqdn)
		}
	}
	plist, err := peers.Load(filepath.Join(r, "config", "peers.db"))
	if err != nil {
		return 0, err
	}
	taken := map[string]bool{"hub": true}
	for _, p := range plist {
		taken[p.Name] = true
	}
	if taken[name] {
		return 0, c.failf("'%s' is already served by MagicDNS (peer or hub name)", name)
	}
	records = append(records, customRecord{Name: fqdn, IP: addr.String()})
	if err := saveCustomRecords(r, records); err != nil {
		return 0, err
	}
	c.reloadDNS(r, env)
	c.say("dns records reloaded")
	c.say("added DNS record %s -> %s", fqdn, addr.String())
	c.ok("name", fqdn, "ip", addr.String())
	return 0, nil
}

func (c *Cli) cmdDnsRemove(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = fmt.Errorf("expected exactly one positional argument: name")
		}
		return 2, c.argErr("dns remove", err)
	}
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	env, err := envfile.Load(envFile)
	if err != nil {
		return 0, err
	}
	name := pos[0]
	if !strings.Contains(name, ".") {
		name += "." + dnsDomain(env)
	}
	records, err := loadCustomRecords(r)
	if err != nil {
		return 0, err
	}
	found := false
	kept := records[:0]
	for _, rec := range records {
		if rec.Name == name {
			found = true
			continue
		}
		kept = append(kept, rec)
	}
	if !found {
		return 0, c.failf("no such record: %s", name)
	}
	if err := saveCustomRecords(r, kept); err != nil {
		return 0, err
	}
	c.reloadDNS(r, env)
	c.say("dns records reloaded")
	c.say("removed DNS record %s", name)
	c.ok("removed", name)
	return 0, nil
}

func (c *Cli) cmdDnsList(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 0 {
		return 2, c.argErr("dns list", err)
	}
	r := root()
	if _, err := c.requireInit(r); err != nil {
		return 0, err
	}
	records, err := loadCustomRecords(r)
	if err != nil {
		return 0, err
	}
	if !c.JSON && len(records) == 0 {
		c.out("no custom records")
	}
	for _, rec := range records {
		c.out(fmt.Sprintf("%-40s %s", rec.Name, rec.IP))
	}
	objs := make([]any, 0, len(records))
	for _, rec := range records {
		o := ojson.New()
		o.Set("name", rec.Name)
		o.Set("ip", rec.IP)
		objs = append(objs, o)
	}
	c.ok("records", objs)
	return 0, nil
}
