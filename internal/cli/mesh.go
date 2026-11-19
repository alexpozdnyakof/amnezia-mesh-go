package cli

// mesh link|endpoint|config|status — explicit device-to-device links
// (Epic 4, stage T1). Mirrors awg_mesh/cli.py byte for byte.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/mesh"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/profile"
)

func (c *Cli) cmdMesh(args []string) (int, error) {
	if len(args) == 0 {
		usage(c.Stderr)
		return 2, nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "link":
		return c.cmdMeshSub("link", rest, map[string]func([]string) (int, error){
			"add": c.cmdMeshLinkAdd, "remove": c.cmdMeshLinkRemove, "list": c.cmdMeshLinkList})
	case "endpoint":
		return c.cmdMeshSub("endpoint", rest, map[string]func([]string) (int, error){
			"set": c.cmdMeshEndpointSet, "unset": c.cmdMeshEndpointUnset, "list": c.cmdMeshEndpointList})
	case "config":
		return c.cmdMeshConfig(rest)
	case "status":
		return c.cmdMeshStatus(rest)
	default:
		fmt.Fprintf(c.Stderr, "awg-mesh: error: unknown mesh command: %s\n", sub)
		usage(c.Stderr)
		return 2, nil
	}
}

func (c *Cli) cmdMeshSub(group string, args []string, cmds map[string]func([]string) (int, error)) (int, error) {
	if len(args) == 0 {
		usage(c.Stderr)
		return 2, nil
	}
	fn, ok := cmds[args[0]]
	if !ok {
		fmt.Fprintf(c.Stderr, "awg-mesh: error: unknown mesh %s command: %s\n", group, args[0])
		usage(c.Stderr)
		return 2, nil
	}
	return fn(args[1:])
}

func peerNames(r string) (map[string]bool, error) {
	plist, err := peers.Load(filepath.Join(r, "config", "peers.db"))
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(plist))
	for _, p := range plist {
		names[p.Name] = true
	}
	return names, nil
}

type p2pResult struct {
	written         []string
	managed, static []mesh.Direct
	skipped         []string
}

// buildP2P writes <name>-mesh-p2p.{conf,vpn}. A *mesh.Error is returned when
// the device has no identity to reuse.
func (c *Cli) buildP2P(r, envFile, name string) (*p2pResult, error) {
	plist, err := peers.Load(filepath.Join(r, "config", "peers.db"))
	if err != nil {
		return nil, err
	}
	var peer *peers.Peer
	infos := make([]mesh.PeerInfo, 0, len(plist))
	for i, p := range plist {
		infos = append(infos, mesh.PeerInfo{Name: p.Name, IP: p.IP, Pub: p.Pub})
		if p.Name == name {
			peer = &plist[i]
		}
	}
	if peer == nil {
		return nil, mesh.Errorf("no such peer: %s", name)
	}
	prefix := filepath.Join(r, "config", "peers", name)
	priv, psk, err := mesh.ReadIdentity(prefix + "-mesh.conf")
	if err != nil {
		return nil, err
	}
	managed, static, skipped, err := mesh.DirectPeers(r, name, infos, c.utcNow())
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		return nil, err
	}
	written, err := profile.GenerateP2PProfile(envFile, name, peer.IP, priv, peer.Pub, psk, managed, static, prefix)
	if err != nil {
		return nil, err
	}
	return &p2pResult{written, managed, static, skipped}, nil
}

// refreshP2P re-renders p2p profiles after a topology/endpoint/key change.
// Devices without links lose their p2p files; devices without an identity
// are reported, never fatal (the change itself already succeeded).
func (c *Cli) refreshP2P(r, envFile string, names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		known, err := peerNames(r)
		if err != nil {
			return err
		}
		if !known[name] {
			continue
		}
		parts, err := mesh.Partners(r, name)
		if err != nil {
			return err
		}
		prefix := filepath.Join(r, "config", "peers", name)
		if len(parts) == 0 {
			for _, suffix := range []string{"mesh-p2p.conf", "mesh-p2p.vpn"} {
				os.Remove(prefix + "-" + suffix)
			}
			continue
		}
		if _, err := c.buildP2P(r, envFile, name); err != nil {
			var me *mesh.Error
			if errors.As(err, &me) {
				c.say("warning: p2p profile for '%s' not rendered: %s", name, me.Msg)
				continue
			}
			return err
		}
	}
	return nil
}

func (c *Cli) cmdMeshLinkAdd(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = fmt.Errorf("expected: mesh link add <a> <b>")
		}
		return 2, c.argErr("mesh link add", err)
	}
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	a, b := pos[0], pos[1]
	names, err := peerNames(r)
	if err != nil {
		return 0, err
	}
	if err := mesh.AddLink(r, a, b, names, c.utcNow()); err != nil {
		return 0, err
	}
	if err := c.refreshP2P(r, envFile, []string{a, b}); err != nil {
		return 0, err
	}
	c.say("linked %s <-> %s; re-import the p2p profile on both devices (managed agents pick it up on their own)", a, b)
	c.ok("linked", []any{a, b})
	return 0, nil
}

func (c *Cli) cmdMeshLinkRemove(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = fmt.Errorf("expected: mesh link remove <a> <b>")
		}
		return 2, c.argErr("mesh link remove", err)
	}
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	a, b := pos[0], pos[1]
	if err := mesh.RemoveLink(r, a, b); err != nil {
		return 0, err
	}
	if err := c.refreshP2P(r, envFile, []string{a, b}); err != nil {
		return 0, err
	}
	c.say("unlinked %s <-> %s; re-import the p2p profile on both devices", a, b)
	c.ok("unlinked", []any{a, b})
	return 0, nil
}

func linkObj(l mesh.Link) *ojson.Obj {
	o := ojson.New()
	o.Set("a", l.A)
	o.Set("b", l.B)
	o.Set("created", l.Created)
	return o
}

func (c *Cli) cmdMeshLinkList(args []string) (int, error) {
	if _, pos, err := parseArgs(args, nil); err != nil || len(pos) != 0 {
		return 2, c.argErr("mesh link list", err)
	}
	r := root()
	if _, err := c.requireInit(r); err != nil {
		return 0, err
	}
	links, err := mesh.LoadLinks(r)
	if err != nil {
		return 0, err
	}
	if len(links) == 0 {
		c.out("no links")
	}
	arr := []any{}
	for _, l := range links {
		c.out(fmt.Sprintf("%-20s <-> %-20s %s", l.A, l.B, l.Created))
		arr = append(arr, linkObj(l))
	}
	c.ok("links", arr)
	return 0, nil
}

func (c *Cli) cmdMeshEndpointSet(args []string) (int, error) {
	flags, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 2 {
		if err == nil {
			err = fmt.Errorf("expected: mesh endpoint set <name> <host:port> [--note T]")
		}
		return 2, c.argErr("mesh endpoint set", err)
	}
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	names, err := peerNames(r)
	if err != nil {
		return 0, err
	}
	if err := mesh.SetStatic(r, pos[0], pos[1], flags["note"], names); err != nil {
		return 0, err
	}
	affected, err := mesh.Partners(r, pos[0])
	if err != nil {
		return 0, err
	}
	if err := c.refreshP2P(r, envFile, affected); err != nil {
		return 0, err
	}
	if len(affected) > 0 {
		c.say("partner profiles re-rendered: %s", strings.Join(affected, ", "))
	}
	norm, err := mesh.ParseEndpoint(pos[1])
	if err != nil {
		return 0, err
	}
	c.ok("name", pos[0], "endpoint", norm, "affected", strList(affected))
	return 0, nil
}

func (c *Cli) cmdMeshEndpointUnset(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = fmt.Errorf("expected: mesh endpoint unset <name>")
		}
		return 2, c.argErr("mesh endpoint unset", err)
	}
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	if err := mesh.UnsetStatic(r, pos[0]); err != nil {
		return 0, err
	}
	affected, err := mesh.Partners(r, pos[0])
	if err != nil {
		return 0, err
	}
	if err := c.refreshP2P(r, envFile, affected); err != nil {
		return 0, err
	}
	if len(affected) > 0 {
		c.say("partner profiles re-rendered: %s", strings.Join(affected, ", "))
	}
	c.ok("unset", pos[0], "affected", strList(affected))
	return 0, nil
}

// strList converts to []any so ojson renders [] for an empty list.
func strList(ss []string) []any {
	out := []any{}
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func nullable(s string, ok bool) any {
	if !ok {
		return nil
	}
	return s
}

func (c *Cli) cmdMeshEndpointList(args []string) (int, error) {
	if _, pos, err := parseArgs(args, nil); err != nil || len(pos) != 0 {
		return 2, c.argErr("mesh endpoint list", err)
	}
	r := root()
	if _, err := c.requireInit(r); err != nil {
		return 0, err
	}
	static, err := mesh.LoadStatic(r)
	if err != nil {
		return 0, err
	}
	observed, err := mesh.LoadObserved(r, c.utcNow())
	if err != nil {
		return 0, err
	}
	names, err := peerNames(r)
	if err != nil {
		return 0, err
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	rows := []any{}
	for _, name := range sorted {
		ep, src, found := mesh.ResolveEndpoint(name, static, observed)
		var stEp *string
		note := ""
		for _, s := range static {
			if s.Name == name {
				e := s.Endpoint
				stEp = &e
				note = s.Note
			}
		}
		obsEp, obsOK := observed[name]
		row := ojson.New()
		row.Set("name", name)
		row.Set("endpoint", nullable(ep, found))
		row.Set("source", nullable(src, found))
		row.Set("static", nullable(deref(stEp), stEp != nil))
		row.Set("observed", nullable(obsEp, obsOK))
		row.Set("note", note)
		rows = append(rows, row)
		shown, label := "-", "none"
		if found {
			shown, label = ep, src
		}
		c.out(fmt.Sprintf("%-20s %-24s %s", name, shown, label))
	}
	c.ok("endpoints", rows)
	return 0, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func directNames(ds []mesh.Direct) []any {
	out := []any{}
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func (c *Cli) cmdMeshConfig(args []string) (int, error) {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		if err == nil {
			err = fmt.Errorf("expected: mesh config <device>")
		}
		return 2, c.argErr("mesh config", err)
	}
	device := pos[0]
	r := root()
	envFile, err := c.requireInit(r)
	if err != nil {
		return 0, err
	}
	parts, err := mesh.Partners(r, device)
	if err != nil {
		return 0, err
	}
	if len(parts) == 0 {
		return 0, c.failf("%s has no links (see: awg-mesh mesh link add)", device)
	}
	res, err := c.buildP2P(r, envFile, device)
	if err != nil {
		return 0, err
	}
	c.out("")
	c.out(fmt.Sprintf("wrote %s (managed: %d direct peers)", res.written[0], len(res.managed)))
	c.out(fmt.Sprintf("wrote %s (app, experimental: %d static peers)", res.written[1], len(res.static)))
	if len(res.skipped) > 0 {
		c.out("no endpoint yet (hub-only until set): " + strings.Join(res.skipped, ", "))
	}
	written := make([]any, len(res.written))
	for i, w := range res.written {
		written[i] = w
	}
	c.ok("written", written, "managed", directNames(res.managed),
		"static", directNames(res.static), "skipped", strList(res.skipped))
	return 0, nil
}

func (c *Cli) cmdMeshStatus(args []string) (int, error) {
	if _, pos, err := parseArgs(args, nil); err != nil || len(pos) != 0 {
		return 2, c.argErr("mesh status", err)
	}
	r := root()
	if _, err := c.requireInit(r); err != nil {
		return 0, err
	}
	static, err := mesh.LoadStatic(r)
	if err != nil {
		return 0, err
	}
	observed, err := mesh.LoadObserved(r, c.utcNow())
	if err != nil {
		return 0, err
	}
	links, err := mesh.LoadLinks(r)
	if err != nil {
		return 0, err
	}
	if len(links) == 0 {
		c.out("no links")
	}
	rows := []any{}
	for _, l := range links {
		endpoints := ojson.New()
		var missing []string
		for _, n := range []string{l.A, l.B} {
			ep, src, found := mesh.ResolveEndpoint(n, static, observed)
			e := ojson.New()
			e.Set("endpoint", nullable(ep, found))
			e.Set("source", nullable(src, found))
			endpoints.Set(n, e)
			if !found {
				missing = append(missing, n)
			}
		}
		row := ojson.New()
		row.Set("a", l.A)
		row.Set("b", l.B)
		row.Set("endpoints", endpoints)
		row.Set("direct_capable", len(missing) == 0)
		row.Set("missing", strList(missing))
		rows = append(rows, row)
		verdict := "direct-capable"
		if len(missing) > 0 {
			verdict = "hub-only (no endpoint for " + strings.Join(missing, ", ") + ")"
		}
		c.out(fmt.Sprintf("%-20s <-> %-20s %s", l.A, l.B, verdict))
	}
	c.ok("links", rows)
	return 0, nil
}
