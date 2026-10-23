// Package peers manages peers.db — TSV state: name, ip, pub, psk, created.
//
// Writes go through WithLock, which holds an exclusive flock for the whole
// read-modify-write window, so two concurrent `peer add` runs serialize
// instead of interleaving torn lines into the db.
package peers

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// Peer is one row of peers.db.
type Peer struct {
	Name    string
	IP      string
	Pub     string
	PSK     string
	Created string
}

var validName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidName reports whether name is a DNS label: ^[a-z0-9-]{1,32}$.
func ValidName(name string) bool {
	return validName.MatchString(name)
}

// Parse parses peers.db content (TSV: name, ip, pub, psk, created).
// Lines with fewer than 5 fields are skipped; extra fields are dropped.
func Parse(text string) ([]Peer, error) {
	var out []Peer
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		parts := strings.Split(line, "\t")
		if len(parts) < 5 {
			continue
		}
		p := Peer{Name: parts[0], IP: parts[1], Pub: parts[2], PSK: parts[3], Created: parts[4]}
		if !ValidName(p.Name) {
			return nil, fmt.Errorf("invalid peer name in db: %q", p.Name)
		}
		out = append(out, p)
	}
	return out, nil
}

// FormatLine serializes a peer back to its TSV row.
func FormatLine(p Peer) string {
	return p.Name + "\t" + p.IP + "\t" + p.Pub + "\t" + p.PSK + "\t" + p.Created + "\n"
}

// Tx is a mutable peer list; written back on WithLock exit only when dirty.
type Tx struct {
	Peers []Peer
	dirty bool
}

// Add appends a peer and marks the tx dirty.
func (t *Tx) Add(p Peer) {
	t.Peers = append(t.Peers, p)
	t.dirty = true
}

// Remove drops a peer by name. Returns false (and stays clean) if absent.
func (t *Tx) Remove(name string) bool {
	kept := t.Peers[:0]
	removed := false
	for _, p := range t.Peers {
		if p.Name == name {
			removed = true
			continue
		}
		kept = append(kept, p)
	}
	if removed {
		t.Peers = kept
		t.dirty = true
	}
	return removed
}

// Rename renames a peer. Returns false if old is missing or new is taken.
func (t *Tx) Rename(old, new string) bool {
	if t.Find(new) != nil {
		return false
	}
	p := t.Find(old)
	if p == nil {
		return false
	}
	p.Name = new
	t.dirty = true
	return true
}

// Find returns the peer with the given name, or nil.
func (t *Tx) Find(name string) *Peer {
	for i := range t.Peers {
		if t.Peers[i].Name == name {
			return &t.Peers[i]
		}
	}
	return nil
}

// WithLock runs fn as an exclusive-locking read-modify-write transaction
// over peers.db at path (created on first write, like the Python locked()).
func WithLock(path string, fn func(*Tx) error) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:errcheck // best-effort unlock

	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	plist, err := Parse(string(data))
	if err != nil {
		return err
	}
	tx := &Tx{Peers: plist}
	if err := fn(tx); err != nil {
		return err
	}
	if !tx.dirty {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	var w strings.Builder
	for _, p := range tx.Peers {
		w.WriteString(FormatLine(p))
	}
	if _, err := f.WriteString(w.String()); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

// Load reads peers.db; a missing file yields an empty list (not an error).
func Load(path string) ([]Peer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return Parse(string(data))
}

// ErrNoFreeIP reports subnet exhaustion (Python returns None).
var ErrNoFreeIP = errors.New("no free IP in subnet")

// NextFreeIP returns the first free host IP of the subnet; the hub (.1) is
// reserved. Ok is false on exhaustion.
func NextFreeIP(subnet string, plist []Peer) (ip string, ok bool, err error) {
	p, err := parseNetwork(subnet)
	if err != nil {
		return "", false, err
	}
	used := make(map[netip.Addr]struct{}, len(plist))
	for _, pr := range plist {
		a, err := netip.ParseAddr(pr.IP)
		if err != nil {
			return "", false, err
		}
		used[a] = struct{}{}
	}
	h := pyHosts(p)
	for _, a := range h[min(1, len(h)):] { // skip .1: hub
		if _, taken := used[a]; !taken {
			return a.String(), true, nil
		}
	}
	return "", false, nil
}

// parseNetwork mirrors Python's ipaddress.ip_network(subnet) with the
// default strict=True: host bits set → error.
func parseNetwork(subnet string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		return netip.Prefix{}, err
	}
	if p.Addr() != p.Masked().Addr() {
		return netip.Prefix{}, fmt.Errorf("%s has host bits set", subnet)
	}
	return p.Masked(), nil
}

// pyHosts replicates Python's ipaddress hosts(): for prefixlen < /31 the
// range is network+1..broadcast-1; /31 yields both addresses; /32 the one.
func pyHosts(p netip.Prefix) []netip.Addr {
	first := p.Masked().Addr()
	n := maxBits(first)
	last := lastAddr(p)
	var out []netip.Addr
	if p.Bits() >= n-1 {
		for a := first; ; a = a.Next() {
			out = append(out, a)
			if a == last {
				break
			}
		}
		return out
	}
	for a := first.Next(); ; a = a.Next() {
		out = append(out, a)
		if a == last.Prev() {
			break
		}
	}
	return out
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr()
	if a.Is4() {
		b := a.As4()
		for i := p.Bits(); i < 32; i++ {
			b[i/8] |= 1 << uint(7-i%8)
		}
		return netip.AddrFrom4(b)
	}
	b := a.As16()
	for i := p.Bits(); i < 128; i++ {
		b[i/8] |= 1 << uint(7-i%8)
	}
	return netip.AddrFrom16(b)
}

func maxBits(a netip.Addr) int {
	if a.Is4() {
		return 32
	}
	return 128
}
