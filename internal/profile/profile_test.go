package profile

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/ojson"
)

const fixturesDir = "../../testdata/fixtures"

var (
	priv = strings.Repeat("A", 44) // shape-only stand-ins; nothing here touches real keys
	pub  = strings.Repeat("B", 44)
	psk  = strings.Repeat("C", 44)
)

func gen(t *testing.T) (written []string, prefix string) {
	t.Helper()
	prefix = filepath.Join(t.TempDir(), "peer")
	written, err := GenerateProfiles(
		filepath.Join(fixturesDir, "server.env"), "laptop", "100.64.0.7",
		priv, pub, psk, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return written, prefix
}

func awgBlock(t *testing.T, prof *ojson.Obj) *ojson.Obj {
	t.Helper()
	containers, ok := prof.Get("containers").([]any)
	if !ok || len(containers) == 0 {
		t.Fatal("no containers")
	}
	c0, ok := containers[0].(*ojson.Obj)
	if !ok {
		t.Fatal("container[0] not an object")
	}
	awg, ok := c0.Get("awg").(*ojson.Obj)
	if !ok {
		t.Fatal("no awg block")
	}
	return awg
}

func lastConfig(t *testing.T, prof *ojson.Obj) *ojson.Obj {
	t.Helper()
	lc, ok := awgBlock(t, prof).Get("last_config").(string)
	if !ok {
		t.Fatal("last_config not a string")
	}
	lcObj, err := ojson.ParseObj([]byte(lc))
	if err != nil {
		t.Fatal(err)
	}
	return lcObj
}

func TestGenWritesFourProfiles(t *testing.T) {
	written, prefix := gen(t)
	want := map[string]bool{
		prefix + "-exit.vpn": false, prefix + "-mesh.vpn": false,
		prefix + "-exit.conf": false, prefix + "-mesh.conf": false,
	}
	if len(written) != 4 {
		t.Fatalf("want 4 files, got %v", written)
	}
	for _, path := range written {
		if _, ok := want[path]; !ok {
			t.Fatalf("unexpected path: %s", path)
		}
		want[path] = true
		fi, err := os.Stat(path)
		if err != nil || fi.Size() == 0 {
			t.Fatalf("%s: missing or empty (err=%v)", path, err)
		}
	}
	for path, seen := range want {
		if !seen {
			t.Fatalf("not written: %s", path)
		}
	}
}

func TestVPNProfileStructureAndRouting(t *testing.T) {
	_, prefix := gen(t)
	for _, ptype := range []string{"exit", "mesh"} {
		data, err := os.ReadFile(prefix + "-" + ptype + ".vpn")
		if err != nil {
			t.Fatal(err)
		}
		prof, err := DecodeProfile(string(data))
		if err != nil {
			t.Fatal(err)
		}

		if got := awgBlock(t, prof).Get("protocol_version"); got != "3.1" {
			t.Fatalf("protocol_version = %v", got)
		}
		if got := awgBlock(t, prof).Get("transport_proto"); got != "udp" {
			t.Fatalf("transport_proto = %v", got)
		}
		containers := prof.Get("containers").([]any)
		c0 := containers[0].(*ojson.Obj)
		if got := c0.Get("container"); got != "amnezia-awg2" {
			t.Fatalf("container = %v", got)
		}
		if got := prof.Get("hostName"); got != "192.0.2.10" {
			t.Fatalf("hostName = %v", got)
		}
		if got := prof.Get("dns1"); got != "100.64.0.1" { // hub answers MagicDNS
			t.Fatalf("dns1 = %v", got)
		}
		if desc, ok := prof.Get("description").(string); !ok || !strings.Contains(desc, "laptop") {
			t.Fatalf("description = %v", prof.Get("description"))
		}
	}

	exit, err := DecodeProfile(mustRead(t, prefix+"-exit.vpn"))
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := DecodeProfile(mustRead(t, prefix+"-mesh.vpn"))
	if err != nil {
		t.Fatal(err)
	}
	exitLC := lastConfig(t, exit)
	meshLC := lastConfig(t, mesh)
	if got := exitLC.Get("allowed_ips"); !ojson.Equal(got, []any{"0.0.0.0/0"}) {
		t.Fatalf("exit allowed_ips = %v", got)
	}
	if got := meshLC.Get("allowed_ips"); !ojson.Equal(got, []any{"100.64.0.0/24"}) {
		t.Fatalf("mesh allowed_ips = %v", got)
	}
	if got := exitLC.Get("client_ip"); got != "100.64.0.7" {
		t.Fatalf("client_ip = %v", got)
	}
	conf, ok := exitLC.Get("config").(string)
	if !ok || !strings.Contains(conf, "Endpoint = 192.0.2.10:443") {
		t.Fatalf("config missing endpoint:\n%s", conf)
	}
}

func TestRoundtripIsSemanticallyStable(t *testing.T) {
	_, prefix := gen(t)
	for _, ptype := range []string{"exit", "mesh"} {
		decoded, err := DecodeProfile(mustRead(t, prefix+"-"+ptype+".vpn"))
		if err != nil {
			t.Fatal(err)
		}
		reencoded, err := EncodeProfile(decoded)
		if err != nil {
			t.Fatal(err)
		}
		redecoded, err := DecodeProfile(reencoded)
		if err != nil {
			t.Fatal(err)
		}
		if !ojson.Equal(decoded, redecoded) {
			t.Fatalf("%s: re-encoded profile differs semantically", ptype)
		}
	}
}

func TestConfVariantMatchesEmbeddedConfig(t *testing.T) {
	_, prefix := gen(t)
	for _, ptype := range []string{"exit", "mesh"} {
		prof, err := DecodeProfile(mustRead(t, prefix+"-"+ptype+".vpn"))
		if err != nil {
			t.Fatal(err)
		}
		embedded, ok := lastConfig(t, prof).Get("config").(string)
		if !ok {
			t.Fatal("no embedded config")
		}
		conf := mustRead(t, prefix+"-"+ptype+".conf")
		if embedded != conf {
			t.Fatalf("%s.conf diverged from embedded config", ptype)
		}
	}
}

// Cross-implementation parity: the same inputs run through the Python
// reference produce golden artifacts; Go must match semantically (.vpn)
// and byte-for-byte (.conf — pure text, no deflate involved).
func TestAgainstPythonGolden(t *testing.T) {
	_, prefix := gen(t)
	golden := filepath.Join(fixturesDir, "golden-profile", "peer")
	for _, ptype := range []string{"exit", "mesh"} {
		goConf := mustRead(t, prefix+"-"+ptype+".conf")
		pyConf := mustRead(t, golden+"-"+ptype+".conf")
		if goConf != pyConf {
			t.Fatalf("%s.conf diverged from Python reference:\n--- go ---\n%s\n--- python ---\n%s",
				ptype, goConf, pyConf)
		}
		goProf, err := DecodeProfile(mustRead(t, prefix+"-"+ptype+".vpn"))
		if err != nil {
			t.Fatal(err)
		}
		pyProf, err := DecodeProfile(mustRead(t, golden+"-"+ptype+".vpn"))
		if err != nil {
			t.Fatal(err)
		}
		if !ojson.Equal(goProf, pyProf) {
			t.Fatalf("%s.vpn diverged from Python reference semantically", ptype)
		}
	}
}

// Adversarial: unicode device names flow into description/config; the codec
// must survive CPython-style \u-escaping both directions.
func TestUnicodeNameRoundtrip(t *testing.T) {
	env := craftEnv()
	prof, err := BuildAppProfile(env, "ünïcode-手机", "exit", "100.64.0.7",
		"0.0.0.0/0", priv, pub, psk, "D"+pub[1:])
	if err != nil {
		t.Fatal(err)
	}
	url, err := EncodeProfile(prof)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProfile(url)
	if err != nil {
		t.Fatal(err)
	}
	desc, ok := decoded.Get("description").(string)
	if !ok || desc != "192.0.2.10 (ünïcode-手机 exit)" {
		t.Fatalf("description = %v", decoded.Get("description"))
	}
	reencoded, err := EncodeProfile(decoded)
	if err != nil {
		t.Fatal(err)
	}
	redecoded, err := DecodeProfile(reencoded)
	if err != nil {
		t.Fatal(err)
	}
	if !ojson.Equal(decoded, redecoded) {
		t.Fatal("unicode roundtrip changed semantics")
	}
}

// Adversarial: obfuscation values at the top of the 32-bit range
// (H-parameters go up to 2^32-1) must survive as strings.
func TestLargeObfValues(t *testing.T) {
	env := craftEnv()
	env["OBF_H4"] = "4294967295"
	env["OBF_JC"] = "7"
	prof, err := BuildAppProfile(env, "dev", "mesh", "100.64.0.8",
		env["AWG_MESH_SUBNET"], priv, pub, psk, "D"+pub[1:])
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProfile(mustEncode(t, prof))
	if err != nil {
		t.Fatal(err)
	}
	awg := awgBlock(t, decoded)
	if got := awg.Get("H4"); got != "4294967295" {
		t.Fatalf("H4 = %v", got)
	}
	if got := lastConfig(t, decoded).Get("Jc"); got != "7" {
		t.Fatalf("Jc = %v", got)
	}
}

// Adversarial: every base64 padding class (len%4 == 0,1,2,3) must decode;
// encoded output must never contain padding characters.
func TestBase64PaddingEdges(t *testing.T) {
	env := craftEnv()
	for i := 0; i < 8; i++ {
		prof, err := BuildAppProfile(env, strings.Repeat("x", i), "exit",
			"100.64.0.7", "0.0.0.0/0", priv, pub, psk, "D"+pub[1:])
		if err != nil {
			t.Fatal(err)
		}
		url, err := EncodeProfile(prof)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(url, "=") {
			t.Fatalf("padding not stripped: %q...", url[:24])
		}
		decoded, err := DecodeProfile("  " + url + "\n") // whitespace-tolerant
		if err != nil {
			t.Fatalf("pad case %d: %v", i%4, err)
		}
		if !ojson.Equal(prof, decoded) {
			t.Fatalf("pad case %d: semantic mismatch", i%4)
		}
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	cases := []string{
		"vpn://!!!not-base64!!!",
		"vpn://AAAA",     // valid b64, but < 4 decoded bytes is impossible here; truncated frame
		"vpn://AAAAAA==", // 4 bytes: length prefix claims a huge payload
		"vpn://" + base64Of([]byte{0, 0, 0, 5, 'z', 'z'}), // zlib garbage
	}
	for _, c := range cases {
		if _, err := DecodeProfile(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func craftEnv() map[string]string {
	return map[string]string{
		"AWG_MESH_SUBNET": "100.64.0.0/24",
		"AWG_MESH_HOST":   "192.0.2.10",
		"AWG_MESH_PORT":   "443",
		"HUB_IP":          "100.64.0.1",
		"MTU":             "1280",
		"SERVER_PUB":      "D" + pub[1:],
		"OBF_JC":          "5",
		"OBF_H1":          "1234567890",
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustEncode(t *testing.T, prof *ojson.Obj) string {
	t.Helper()
	url, err := EncodeProfile(prof)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func base64Of(b []byte) string {
	var buf bytes.Buffer
	enc := base64.NewEncoder(base64.URLEncoding, &buf)
	enc.Write(b)
	enc.Close()
	return buf.String()
}
