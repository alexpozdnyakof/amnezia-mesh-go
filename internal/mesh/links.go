package mesh

// Explicit device-to-device links (Epic 4, stage T1) — port of
// awg_mesh/mesh.py. Three json files under config/, one owner each:
//
//	mesh-links.json      operator (CLI)   topology: explicit unordered pairs
//	mesh-static.json     operator (CLI)   stable endpoints, hand-marked
//	mesh-endpoints.json  collector        observed endpoints (read-only here)
//
// No private keys live in any of them. Writes are atomic (tmp + rename) and
// byte-identical to Python's json.dump(indent=2) + "\n".

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
)

const (
	LinksFile    = "mesh-links.json"
	StaticFile   = "mesh-static.json"
	ObservedFile = "mesh-endpoints.json"

	// An observed endpoint is usable only while the hub saw a recent
	// handshake from it and the collector snapshot itself is fresh.
	ObservedMaxHandshakeAge = 300
	ObservedMaxFileAge      = 600
)

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// Link is one explicit unordered device pair.
type Link struct {
	A, B, Created string
}

// Static is one hand-marked stable endpoint (insertion-ordered in the file).
type Static struct {
	Name, Endpoint, Note string
}

// Direct is a linked partner as render input.
type Direct struct {
	Name, Pub, IP, Endpoint, Source string
}

func path(r, fname string) string { return filepath.Join(r, "config", fname) }

// read returns the parsed file, or nil when absent.
func read(r, fname string) (*ojson.Obj, error) {
	data, err := os.ReadFile(path(r, fname))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o, err := ojson.ParseObj(data)
	if err != nil {
		return nil, Errorf("config/%s is not valid json: %v", fname, err)
	}
	if v, ok := o.Get("version").(json.Number); !ok || v.String() != "1" {
		return nil, Errorf("config/%s: unsupported format (want version 1)", fname)
	}
	return o, nil
}

func write(r, fname string, o *ojson.Obj) error {
	p := path(r, fname)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	o.MarshalIndent(&buf)
	buf.WriteByte('\n')
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func samePair(l Link, a, b string) bool {
	return (l.A == a && l.B == b) || (l.A == b && l.B == a)
}

// --- links -------------------------------------------------------------------

func LoadLinks(r string) ([]Link, error) {
	o, err := read(r, LinksFile)
	if err != nil || o == nil {
		return nil, err
	}
	arr, _ := o.Get("links").([]any)
	var links []Link
	for _, e := range arr {
		lo, ok := e.(*ojson.Obj)
		if !ok {
			return nil, Errorf("config/%s: malformed link entry", LinksFile)
		}
		a, _ := lo.Get("a").(string)
		b, _ := lo.Get("b").(string)
		c, _ := lo.Get("created").(string)
		links = append(links, Link{a, b, c})
	}
	return links, nil
}

func SaveLinks(r string, links []Link) error {
	arr := []any{}
	for _, l := range links {
		lo := ojson.New()
		lo.Set("a", l.A)
		lo.Set("b", l.B)
		lo.Set("created", l.Created)
		arr = append(arr, lo)
	}
	o := ojson.New()
	o.Set("version", 1)
	o.Set("links", arr)
	return write(r, LinksFile, o)
}

// AddLink records link a<->b; names are the known peer names.
func AddLink(r, a, b string, names map[string]bool, now string) error {
	if a == b {
		return Errorf("cannot link a device to itself")
	}
	for _, n := range []string{a, b} {
		if !names[n] {
			return Errorf("no such peer: %s", n)
		}
	}
	links, err := LoadLinks(r)
	if err != nil {
		return err
	}
	for _, l := range links {
		if samePair(l, a, b) {
			return Errorf("already linked: %s <-> %s", a, b)
		}
	}
	return SaveLinks(r, append(links, Link{a, b, now}))
}

func RemoveLink(r, a, b string) error {
	links, err := LoadLinks(r)
	if err != nil {
		return err
	}
	var kept []Link
	for _, l := range links {
		if !samePair(l, a, b) {
			kept = append(kept, l)
		}
	}
	if len(kept) == len(links) {
		return Errorf("not linked: %s <-> %s", a, b)
	}
	return SaveLinks(r, kept)
}

// Partners returns names linked to name, in link-creation order.
func Partners(r, name string) ([]string, error) {
	links, err := LoadLinks(r)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range links {
		if l.A == name {
			out = append(out, l.B)
		} else if l.B == name {
			out = append(out, l.A)
		}
	}
	return out, nil
}

// --- static endpoints --------------------------------------------------------

// pyRepr renders s like Python's repr() for the common cases.
func pyRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case string(r) == q:
			b.WriteString(`\` + q)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x` + strconv.FormatInt(int64(r)+0x100, 16)[1:])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseEndpoint validates host:port (IPv4 or hostname) and returns the
// normalized string.
func ParseEndpoint(text string) (string, error) {
	i := strings.LastIndex(text, ":")
	if i < 0 || i == 0 || !isDigits(text[i+1:]) {
		return "", Errorf("invalid endpoint: %s (want host:port)", pyRepr(text))
	}
	host, port := text[:i], text[i+1:]
	trimmed := strings.TrimLeft(port, "0")
	n := 0
	if len(trimmed) > 5 {
		n = 1 << 30
	} else if trimmed != "" {
		n, _ = strconv.Atoi(trimmed)
	}
	if n < 1 || n > 65535 {
		return "", Errorf("invalid endpoint port: %s", port)
	}
	if !hostRe.MatchString(host) {
		return "", Errorf("invalid endpoint host: %s", pyRepr(host))
	}
	return host + ":" + strconv.Itoa(n), nil
}

func LoadStatic(r string) ([]Static, error) {
	o, err := read(r, StaticFile)
	if err != nil || o == nil {
		return nil, err
	}
	eps, _ := o.Get("endpoints").(*ojson.Obj)
	if eps == nil {
		return nil, nil
	}
	var out []Static
	for _, k := range eps.Keys() {
		e, _ := eps.Get(k).(*ojson.Obj)
		if e == nil {
			return nil, Errorf("config/%s: malformed endpoint entry", StaticFile)
		}
		ep, _ := e.Get("endpoint").(string)
		note, _ := e.Get("note").(string)
		out = append(out, Static{k, ep, note})
	}
	return out, nil
}

func SaveStatic(r string, eps []Static) error {
	eo := ojson.New()
	for _, s := range eps {
		e := ojson.New()
		e.Set("endpoint", s.Endpoint)
		e.Set("note", s.Note)
		eo.Set(s.Name, e)
	}
	o := ojson.New()
	o.Set("version", 1)
	o.Set("endpoints", eo)
	return write(r, StaticFile, o)
}

func findStatic(eps []Static, name string) int {
	for i, s := range eps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func SetStatic(r, name, endpoint, note string, names map[string]bool) error {
	if !names[name] {
		return Errorf("no such peer: %s", name)
	}
	eps, err := LoadStatic(r)
	if err != nil {
		return err
	}
	ep, err := ParseEndpoint(endpoint)
	if err != nil {
		return err
	}
	if i := findStatic(eps, name); i >= 0 {
		eps[i] = Static{name, ep, note} // re-set keeps its position
	} else {
		eps = append(eps, Static{name, ep, note})
	}
	return SaveStatic(r, eps)
}

func UnsetStatic(r, name string) error {
	eps, err := LoadStatic(r)
	if err != nil {
		return err
	}
	i := findStatic(eps, name)
	if i < 0 {
		return Errorf("no static endpoint for: %s", name)
	}
	return SaveStatic(r, append(eps[:i], eps[i+1:]...))
}

// --- observed endpoints ------------------------------------------------------

const tsLayout = "2006-01-02T15:04:05Z"

// LoadObserved returns {name: endpoint} for usable observed entries; empty
// when the snapshot is missing or older than ObservedMaxFileAge.
func LoadObserved(r, now string) (map[string]string, error) {
	usable := map[string]string{}
	o, err := read(r, ObservedFile)
	if err != nil || o == nil {
		return usable, err
	}
	upd, _ := o.Get("updated").(string)
	tUpd, err1 := time.Parse(tsLayout, upd)
	tNow, err2 := time.Parse(tsLayout, now)
	if err1 != nil || err2 != nil {
		return usable, nil
	}
	if tNow.Sub(tUpd).Seconds() > ObservedMaxFileAge {
		return usable, nil
	}
	arr, _ := o.Get("peers").([]any)
	for _, e := range arr {
		p, ok := e.(*ojson.Obj)
		if !ok {
			continue
		}
		ep, _ := p.Get("endpoint").(string)
		hs, ok := p.Get("handshake_age").(json.Number)
		if ep == "" || !ok {
			continue
		}
		age, err := hs.Float64()
		if err != nil || age > ObservedMaxHandshakeAge {
			continue
		}
		name, _ := p.Get("name").(string)
		usable[name] = ep
	}
	return usable, nil
}

// ResolveEndpoint: static wins over observed; ok=false if neither.
func ResolveEndpoint(name string, static []Static, observed map[string]string) (endpoint, source string, ok bool) {
	if i := findStatic(static, name); i >= 0 {
		return static[i].Endpoint, "static", true
	}
	if ep, found := observed[name]; found {
		return ep, "observed", true
	}
	return "", "", false
}

// --- lifecycle hooks ---------------------------------------------------------

// PrunePeer (peer revoke) drops the peer's links and static endpoint and
// returns the former partners (they must re-import / re-render).
func PrunePeer(r, name string) ([]string, error) {
	former, err := Partners(r, name)
	if err != nil {
		return nil, err
	}
	if len(former) > 0 {
		links, _ := LoadLinks(r)
		var kept []Link
		for _, l := range links {
			if l.A != name && l.B != name {
				kept = append(kept, l)
			}
		}
		if err := SaveLinks(r, kept); err != nil {
			return nil, err
		}
	}
	eps, err := LoadStatic(r)
	if err != nil {
		return nil, err
	}
	if i := findStatic(eps, name); i >= 0 {
		if err := SaveStatic(r, append(eps[:i], eps[i+1:]...)); err != nil {
			return nil, err
		}
	}
	return former, nil
}

// RenamePeers (peer rename, swaps allowed) rewrites links and static keys.
func RenamePeers(r string, mapping map[string]string) error {
	mp := func(n string) string {
		if v, ok := mapping[n]; ok {
			return v
		}
		return n
	}
	links, err := LoadLinks(r)
	if err != nil {
		return err
	}
	for i := range links {
		links[i].A, links[i].B = mp(links[i].A), mp(links[i].B)
	}
	if len(links) > 0 {
		if err := SaveLinks(r, links); err != nil {
			return err
		}
	}
	eps, err := LoadStatic(r)
	if err != nil {
		return err
	}
	touched := false
	for _, s := range eps {
		if _, ok := mapping[s.Name]; ok {
			touched = true
		}
	}
	if touched {
		var out []Static
		for _, s := range eps { // dict comprehension: last value wins, first position kept
			n := mp(s.Name)
			if i := findStatic(out, n); i >= 0 {
				out[i] = Static{n, s.Endpoint, s.Note}
			} else {
				out = append(out, Static{n, s.Endpoint, s.Note})
			}
		}
		return SaveStatic(r, out)
	}
	return nil
}

// --- profile identity --------------------------------------------------------

// ReadIdentity returns (PrivateKey, hub PresharedKey) from an existing
// client .conf. peers.db holds no device private keys, so a p2p profile
// reuses the identity of the device's already-issued -mesh.conf.
func ReadIdentity(confPath string) (priv, psk string, err error) {
	data, err := os.ReadFile(confPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", Errorf("%s not found: no identity to build a p2p profile from (adopted device? run `peer rotate`)", confPath)
	}
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch strings.TrimSpace(k) {
		case "PrivateKey":
			priv = strings.TrimSpace(v)
		case "PresharedKey":
			psk = strings.TrimSpace(v)
		}
	}
	if priv == "" || psk == "" {
		return "", "", Errorf("%s: missing PrivateKey/PresharedKey", confPath)
	}
	return priv, psk, nil
}

// --- direct-peer selection ---------------------------------------------------

// PeerInfo is the slice of peers.db the selection needs.
type PeerInfo struct{ Name, IP, Pub string }

// DirectPeers returns partners of name as render inputs: managed = partners
// with any live endpoint, static = the subset with a hand-marked one (app
// profile), skipped = partners with no endpoint at all (never guessed).
func DirectPeers(r, name string, plist []PeerInfo, now string) (managed, static []Direct, skipped []string, err error) {
	by := map[string]PeerInfo{}
	for _, p := range plist {
		by[p.Name] = p
	}
	st, err := LoadStatic(r)
	if err != nil {
		return nil, nil, nil, err
	}
	obs, err := LoadObserved(r, now)
	if err != nil {
		return nil, nil, nil, err
	}
	parts, err := Partners(r, name)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, partner := range parts {
		p, ok := by[partner]
		if !ok {
			continue
		}
		ep, src, found := ResolveEndpoint(partner, st, obs)
		if !found {
			skipped = append(skipped, partner)
			continue
		}
		d := Direct{partner, p.Pub, p.IP, ep, src}
		managed = append(managed, d)
		if src == "static" {
			static = append(static, d)
		}
	}
	return managed, static, skipped, nil
}
