// Package adopt converts an existing AmneziaWG/WireGuard server config
// (from a file or a running container) into amnezia-mesh state
// (server.env + peers.db). Everything cryptographic is carried over
// verbatim — server key, obfuscation profile, peer keys and PSKs — so
// existing client profiles keep working unchanged.
package adopt

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/mesh"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/render"
)

// interfaceMap: awg0.conf key -> server.env key for [Interface] lines we
// understand. Address is validated separately and becomes AWG_MESH_SUBNET.
var interfaceMap = map[string]string{
	"PrivateKey": "SERVER_PRIV",
	"Address":    "_ADDRESS",
	"ListenPort": "LISTEN_PORT",
	"MTU":        "MTU",
}

var obfMap = func() map[string]string {
	m := map[string]string{}
	for _, k := range render.ObfKeys() {
		m[k] = "OBF_" + strings.ToUpper(k)
	}
	return m
}()

// Peer comment produced by render.py: "# name (added 2026-10-20T12:00:00Z)".
// Hand-maintained configs use any comment ("# macbook-2 (key rotated ...)"),
// so strip any trailing parenthetical, capturing the date only for
// "(added ...)".
var (
	addedRe          = regexp.MustCompile(`^(.+?)\s*\(added\s+([^)]+)\)\s*$`)
	parenRe          = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	generatedNameLen = 6
)

// PeerRecord is one adopted peer; Created is empty when the comment carried
// no "(added ...)" date (build_state fills it with `now`).
type PeerRecord struct {
	Name    string
	IP      string
	Pub     string
	PSK     string
	Created string
}

// ParsedConf is the result of parsing an awg0.conf.
type ParsedConf struct {
	Env       map[string]string // SERVER_PRIV, LISTEN_PORT, MTU, OBF_*
	Subnet    string            // CIDR, hub == network+1 (asserted)
	Peers     []PeerRecord
	Keepalive string // first peer's PersistentKeepalive ("" if none)
	Warnings  []string
}

func splitComment(text string) (string, string) {
	if m := addedRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	}
	return strings.TrimSpace(parenRe.ReplaceAllString(text, "")), ""
}

// sanitizeName turns comment names into valid DNS-label peer names:
// lowercase, [a-z0-9-]{1,32}. Returns "" when nothing usable remains.
func sanitizeName(name string) string {
	cleaned := nonDNSChars.ReplaceAllString(strings.ToLower(name), "-")
	cleaned = strings.Trim(cleaned, "-")
	if len(cleaned) > 32 {
		cleaned = cleaned[:32]
	}
	cleaned = strings.Trim(cleaned, "-")
	return cleaned
}

var nonDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)

// Parse parses awg0.conf content. It returns an error on anything we cannot
// represent faithfully (unknown [Interface] keys, bad Address, missing peer
// keys) — adopt never drops config silently.
func Parse(text string) (*ParsedConf, error) {
	parsed := &ParsedConf{Env: map[string]string{}}
	section := ""
	var current *PeerRecord
	var pendingComment string
	var unknown []string

	for _, raw := range splitLines(text) {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if section == "Peer" && current != nil && current.Pub == "" {
				pendingComment = strings.TrimSpace(strings.TrimLeft(line, "# "))
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			section = name
			if name == "Peer" {
				current = &PeerRecord{}
				parsed.Peers = append(parsed.Peers, *current)
				current = &parsed.Peers[len(parsed.Peers)-1]
			}
			continue
		}
		if !strings.Contains(line, "=") {
			parsed.Warnings = append(parsed.Warnings,
				fmt.Sprintf("skipped unrecognized line: %q", line))
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)

		if section == "Interface" {
			if envKey, ok := interfaceMap[key]; ok {
				parsed.Env[envKey] = value
			} else if envKey, ok := obfMap[key]; ok {
				parsed.Env[envKey] = value
			} else {
				unknown = append(unknown, key)
			}
		} else if section == "Peer" && current != nil {
			if pendingComment != "" && current.Name == "" {
				current.Name, current.Created = splitComment(pendingComment)
				pendingComment = ""
			}
			switch key {
			case "PublicKey":
				current.Pub = value
			case "PresharedKey":
				current.PSK = value
			case "AllowedIPs":
				first := strings.TrimSpace(strings.Split(value, ",")[0])
				current.IP = strings.Split(first, "/")[0]
			case "PersistentKeepalive":
				if parsed.Keepalive == "" {
					parsed.Keepalive = value
				}
			}
			// other peer keys (e.g. Endpoint) are client-side concerns; ignored
		}
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		uniq := unknown[:0]
		for i, k := range unknown {
			if i == 0 || k != unknown[i-1] {
				uniq = append(uniq, k)
			}
		}
		return nil, mesh.Errorf("unsupported [Interface] keys: %s — extend adopt.INTERFACE_MAP "+
			"or open an issue; refusing to adopt rather than drop them silently",
			strings.Join(uniq, ", "))
	}
	if err := validate(parsed); err != nil {
		return nil, err
	}
	finalizeNames(parsed)
	return parsed, nil
}

// splitLines emulates Python's str.splitlines() for the line endings real
// awg0.conf files have (\n, \r\n).
func splitLines(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func validate(parsed *ParsedConf) error {
	if parsed.Env["SERVER_PRIV"] == "" {
		return mesh.Errorf("no PrivateKey found in [Interface]")
	}
	addr, ok := parsed.Env["_ADDRESS"]
	if !ok || addr == "" {
		return mesh.Errorf("no Address found in [Interface]")
	}
	iface, err := netip.ParsePrefix(addr) // ip_interface(addr); netmask forms unsupported
	if err != nil {
		return mesh.Errorf("invalid Address in [Interface]: %q (%v)", addr, err)
	}
	ip := iface.Addr()
	net := iface.Masked()
	if ip != net.Addr().Next() {
		return mesh.Errorf("server Address %s is not the first host of %s (expected %s) — "+
			"non-standard layout, refusing to guess", ip, net, net.Addr().Next())
	}
	parsed.Subnet = net.String()

	for i := range parsed.Peers {
		peer := &parsed.Peers[i]
		label := peer.Name
		if label == "" {
			label = peer.Pub
		}
		if label == "" {
			label = "?"
		}
		if peer.Pub == "" {
			return mesh.Errorf("peer %s: missing PublicKey", label)
		}
		if peer.PSK == "" {
			return mesh.Errorf("peer %s: missing PresharedKey (mesh profiles require PSKs)", label)
		}
		if peer.IP == "" {
			return mesh.Errorf("peer %s: missing AllowedIPs", label)
		}
		if _, err := netip.ParseAddr(peer.IP); err != nil {
			return mesh.Errorf("peer %s: invalid IP %q", label, peer.IP)
		}
	}
	return nil
}

// finalizeNames fills missing names (peer-xxxxxx from pubkey, sanitized),
// sanitizes comment names, enforces length/dns-label validity, dedupes.
func finalizeNames(parsed *ParsedConf) {
	seen := map[string]bool{}
	for i := range parsed.Peers {
		peer := &parsed.Peers[i]
		var name string
		if peer.Name != "" {
			name = sanitizeName(peer.Name)
			if name == "" {
				parsed.Warnings = append(parsed.Warnings,
					fmt.Sprintf("peer name %q could not be sanitized; falling back to a generated name", peer.Name))
			} else if peer.Name != name {
				parsed.Warnings = append(parsed.Warnings,
					fmt.Sprintf("peer name %q sanitized to %q", peer.Name, name))
			}
		}
		if name == "" {
			// raw pubkey slices contain base64 chars (uppercase, /, +) —
			// sanitize them like any other candidate name
			name = sanitizeName("peer-" + peer.Pub[:generatedNameLen])
			parsed.Warnings = append(parsed.Warnings,
				fmt.Sprintf("peer %s… has no usable name comment; generated %q",
					peer.Pub[:8], name))
		}
		base, counter := name, 2
		for seen[name] {
			name = fmt.Sprintf("%s-%d", base, counter)
			counter++
		}
		seen[name] = true
		peer.Name = name
	}
}

// BuildState returns (env pairs, peer records) ready to be written to
// config/. now is the created-timestamp fallback for peers without one.
func BuildState(parsed *ParsedConf, host string, port int, domain, image, dns1, dns2 string,
	rdpReject bool, serverPub, now string) ([][2]string, []peers.Peer) {
	envPairs := [][2]string{
		{"AWG_MESH_HOST", host},
		{"AWG_MESH_PORT", fmt.Sprintf("%d", port)},
		{"AWG_MESH_SUBNET", parsed.Subnet},
		{"AWG_MESH_DOMAIN", domain},
		{"AWG_MESH_DNS1", dns1},
	}
	if dns2 != "" {
		envPairs = append(envPairs, [2]string{"AWG_MESH_DNS2", dns2})
	}
	envPairs = append(envPairs,
		[2]string{"AWG_MESH_IMAGE", image},
		[2]string{"AWG_MESH_RDP_REJECT", boolBit(rdpReject)},
		[2]string{"AWG_MESH_I_SUPPORTED", "0"},
		[2]string{"LISTEN_PORT", defaultOr(parsed.Env["LISTEN_PORT"], "41480")},
		[2]string{"MTU", defaultOr(parsed.Env["MTU"], "1280")},
		[2]string{"SERVER_KEEPALIVE", parsed.KeepaliveOr("25")},
		[2]string{"CLIENT_KEEPALIVE", "15-20"},
		[2]string{"SERVER_PRIV", parsed.Env["SERVER_PRIV"]},
		[2]string{"SERVER_PUB", serverPub},
	)
	obfKeys := make([]string, 0, len(parsed.Env))
	for k := range parsed.Env {
		if strings.HasPrefix(k, "OBF_") {
			obfKeys = append(obfKeys, k)
		}
	}
	sort.Strings(obfKeys)
	for _, k := range obfKeys {
		envPairs = append(envPairs, [2]string{k, parsed.Env[k]})
	}

	records := make([]peers.Peer, 0, len(parsed.Peers))
	for _, p := range parsed.Peers {
		created := p.Created
		if created == "" {
			created = now
		}
		records = append(records, peers.Peer{
			Name: p.Name, IP: p.IP, Pub: p.Pub, PSK: p.PSK, Created: created,
		})
	}
	return envPairs, records
}

// KeepaliveOr returns the first peer's keepalive, or def when absent.
func (p *ParsedConf) KeepaliveOr(def string) string {
	if p.Keepalive == "" {
		return def
	}
	return p.Keepalive
}

func boolBit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func defaultOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
