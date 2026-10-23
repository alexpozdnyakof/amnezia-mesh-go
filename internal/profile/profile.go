// Package profile implements the Amnezia profile codec for amnezia-mesh:
// native Amnezia app profiles (`vpn://` URLs) that import into the official
// app on any platform, plus plain WireGuard-style .conf files for generic
// awg/wg clients.
//
// vpn:// format (reverse-engineered, verified byte-identical against
// profiles produced by the official Amnezia app):
//
//	vpn:// + base64url_nopad( uint32_be(len(zlib(json, level 9))) + zlib(json) )
//
// with compact JSON separators (",", ":"), produced by internal/ojson to
// match CPython's json.dumps byte-for-byte.
package profile

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/envfile"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
)

// App-internal identifiers, not our docker names: "amnezia-awg2" is the AWG
// service id inside the Amnezia app. Kept verbatim for import compatibility.
const (
	AppContainer       = "amnezia-awg2"
	AppProtocolVersion = "3.1"
	// Legacy display field the app writes into profiles; client_ip is authoritative.
	AppLegacySubnet = "10.8.1.0"
)

var obfKeys = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4",
	"H1", "H2", "H3", "H4", "HeaderProtectionKey",
	"ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout",
	"RejectAfterTime", "KeepaliveTimeout", "MaxHandshakeAttempts",
	"RandomTrailers", "DisableCookies",
}

var iKeys = []string{"I1", "I2", "I3", "I4", "I5"}

// rawGet mirrors Python's env.get(key, def): the default applies only when
// the key is absent, not when it holds an empty string.
func rawGet(env map[string]string, key, def string) string {
	if v, ok := env[key]; ok {
		return v
	}
	return def
}

// EncodeProfile serializes a profile object into a vpn:// URL.
func EncodeProfile(obj *ojson.Obj) (string, error) {
	var inner bytes.Buffer
	obj.Marshal(&inner)
	var zbuf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&zbuf, 9)
	if err != nil {
		return "", err
	}
	if _, err := zw.Write(inner.Bytes()); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	payload := zbuf.Bytes()
	framed := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(framed, uint32(len(payload)))
	copy(framed[4:], payload)
	return "vpn://" + base64.RawURLEncoding.EncodeToString(framed), nil
}

// DecodeProfile parses a vpn:// URL (whitespace-tolerant, prefix optional)
// back into a profile object.
func DecodeProfile(url string) (*ojson.Obj, error) {
	url = strings.TrimSpace(url)
	url = strings.TrimPrefix(url, "vpn://")
	// Python: "=" * (-len(url) % 4) — Go's % keeps the dividend's sign, so
	// write it as a non-negative pad count.
	raw, err := base64.URLEncoding.DecodeString(url + strings.Repeat("=", (4-len(url)%4)%4))
	if err != nil {
		return nil, fmt.Errorf("profile: base64 decode: %w", err)
	}
	if len(raw) < 4 {
		return nil, fmt.Errorf("profile: truncated length header")
	}
	length := binary.BigEndian.Uint32(raw[:4])
	end := min(4+int(length), len(raw))
	zr, err := zlib.NewReader(bytes.NewReader(raw[4:end]))
	if err != nil {
		return nil, fmt.Errorf("profile: zlib: %w", err)
	}
	inner, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("profile: zlib: %w", err)
	}
	return ojson.ParseObj(inner)
}

// RenderClientConf renders a WireGuard-style client config.
func RenderClientConf(env map[string]string, peerIP, allowed, priv, psk, serverPub string) (string, error) {
	hub, ok := env["HUB_IP"]
	if !ok {
		return "", fmt.Errorf("HUB_IP missing in env")
	}
	host, ok := env["AWG_MESH_HOST"]
	if !ok {
		return "", fmt.Errorf("AWG_MESH_HOST missing in env")
	}
	port, ok := env["AWG_MESH_PORT"]
	if !ok {
		return "", fmt.Errorf("AWG_MESH_PORT missing in env")
	}
	mtu := rawGet(env, "MTU", "1280")
	keepalive := rawGet(env, "CLIENT_KEEPALIVE", "15-20")
	lines := []string{
		"[Interface]",
		"Address = " + peerIP + "/32",
		"DNS = " + hub,
		"MTU = " + mtu,
		"PrivateKey = " + priv,
	}
	for _, k := range obfKeys {
		if v := env["OBF_"+strings.ToUpper(k)]; v != "" {
			lines = append(lines, k+" = "+v)
		}
	}
	lines = append(lines,
		"",
		"[Peer]",
		"PublicKey = "+serverPub,
		"PresharedKey = "+psk,
		"AllowedIPs = "+allowed,
		"Endpoint = "+host+":"+port,
		"PersistentKeepalive = "+keepalive,
	)
	return strings.Join(lines, "\n") + "\n", nil
}

// BuildAppProfile builds the app profile object for one variant
// (ptype is "exit" or "mesh"). Key order matches the Python dict insertion
// order byte-for-byte.
func BuildAppProfile(env map[string]string, name, ptype, peerIP, allowed, priv, pub, psk, serverPub string) (*ojson.Obj, error) {
	conf, err := RenderClientConf(env, peerIP, allowed, priv, psk, serverPub)
	if err != nil {
		return nil, err
	}
	host, ok := env["AWG_MESH_HOST"]
	if !ok {
		return nil, fmt.Errorf("AWG_MESH_HOST missing in env")
	}
	port, ok := env["AWG_MESH_PORT"]
	if !ok {
		return nil, fmt.Errorf("AWG_MESH_PORT missing in env")
	}
	hub, ok := env["HUB_IP"]
	if !ok {
		return nil, fmt.Errorf("HUB_IP missing in env")
	}

	// obf keys (present ones, in OBF order) + I1..I5, shared key prefix of
	// both awg blocks — same construction as the Python dict updates.
	setObfAndI := func(o *ojson.Obj) {
		for _, k := range obfKeys {
			if v := env["OBF_"+strings.ToUpper(k)]; v != "" {
				o.Set(k, v)
			}
		}
		for _, k := range iKeys {
			o.Set(k, "")
		}
	}

	lastConfig := ojson.New()
	setObfAndI(lastConfig)
	lastConfig.Set("allowed_ips", []any{allowed})
	lastConfig.Set("clientId", pub)
	lastConfig.Set("client_ip", peerIP)
	lastConfig.Set("client_priv_key", priv)
	lastConfig.Set("client_pub_key", pub)
	lastConfig.Set("config", conf)
	lastConfig.Set("hostName", host)
	lastConfig.Set("mtu", rawGet(env, "MTU", "1280"))
	lastConfig.Set("persistent_keep_alive", rawGet(env, "CLIENT_KEEPALIVE", "15-20"))
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("AWG_MESH_PORT not an int: %w", err)
	}
	lastConfig.Set("port", portNum)
	lastConfig.Set("psk_key", psk)
	lastConfig.Set("server_pub_key", serverPub)

	awg := ojson.New()
	setObfAndI(awg)
	var lcBuf bytes.Buffer
	lastConfig.MarshalSpaced(&lcBuf) // Python: json.dumps(last_config) without separators=
	awg.Set("last_config", lcBuf.String())
	awg.Set("port", port)
	awg.Set("protocol_version", AppProtocolVersion)
	awg.Set("subnet_address", AppLegacySubnet)
	awg.Set("transport_proto", "udp")

	container := ojson.New()
	container.Set("awg", awg)
	container.Set("container", AppContainer)

	prof := ojson.New()
	prof.Set("containers", []any{container})
	prof.Set("defaultContainer", AppContainer)
	prof.Set("description", fmt.Sprintf("%s (%s %s)", host, name, ptype))
	prof.Set("dns1", hub)
	prof.Set("dns2", "")
	prof.Set("hostName", host)
	return prof, nil
}

// GenerateProfiles writes <outPrefix>-{exit,mesh}.{vpn,conf} and returns
// the written paths in that order.
func GenerateProfiles(envFile, name, ip, priv, pub, psk, outPrefix string) ([]string, error) {
	env, err := envfile.Load(envFile)
	if err != nil {
		return nil, err
	}
	p, err := parseNetwork(env["AWG_MESH_SUBNET"])
	if err != nil {
		return nil, err
	}
	env["HUB_IP"] = p.Addr().Next().String()
	serverPub, ok := env["SERVER_PUB"]
	if !ok {
		return nil, fmt.Errorf("SERVER_PUB missing in server.env")
	}
	variants := []struct{ ptype, allowed string }{
		{"exit", "0.0.0.0/0"},
		{"mesh", env["AWG_MESH_SUBNET"]},
	}
	var written []string
	for _, v := range variants {
		prof, err := BuildAppProfile(env, name, v.ptype, ip, v.allowed, priv, pub, psk, serverPub)
		if err != nil {
			return nil, err
		}
		vpn, err := EncodeProfile(prof)
		if err != nil {
			return nil, err
		}
		conf, err := confFromProfile(prof)
		if err != nil {
			return nil, err
		}
		outputs := []struct{ suffix, content string }{
			{v.ptype + ".vpn", vpn},
			{v.ptype + ".conf", conf},
		}
		for _, out := range outputs {
			path := outPrefix + "-" + out.suffix
			if err := os.WriteFile(path, []byte(out.content), 0o644); err != nil {
				return nil, err
			}
			written = append(written, path)
		}
	}
	return written, nil
}

// confFromProfile extracts the embedded client config, like the Python
// json.loads(prof["containers"][0]["awg"]["last_config"])["config"].
func confFromProfile(prof *ojson.Obj) (string, error) {
	containers, ok := prof.Get("containers").([]any)
	if !ok || len(containers) == 0 {
		return "", fmt.Errorf("profile: no containers")
	}
	container, ok := containers[0].(*ojson.Obj)
	if !ok {
		return "", fmt.Errorf("profile: container not an object")
	}
	awg, ok := container.Get("awg").(*ojson.Obj)
	if !ok {
		return "", fmt.Errorf("profile: no awg block")
	}
	lc, ok := awg.Get("last_config").(string)
	if !ok {
		return "", fmt.Errorf("profile: no last_config")
	}
	lcObj, err := ojson.ParseObj([]byte(lc))
	if err != nil {
		return "", err
	}
	conf, ok := lcObj.Get("config").(string)
	if !ok {
		return "", fmt.Errorf("profile: no config in last_config")
	}
	return conf, nil
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
