package adopt_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/adopt"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/mesh"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/peers"
	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/render"
)

const (
	fixturesDir  = "../../testdata/fixtures"
	goldenConf   = fixturesDir + "/golden/awg0.conf"
	priv         = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	pub          = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	psk          = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	serverPubD44 = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
)

const baseConf = `[Interface]
PrivateKey = ` + priv + `
Address = 100.64.0.1/24
ListenPort = 41480
MTU = 1280
Jc = 5

[Peer]
# test-peer (added 2026-10-20T12:00:00Z)
PublicKey = ` + pub + `
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.2/32
PersistentKeepalive = 25
`

func mustParse(t *testing.T, conf string) *adopt.ParsedConf {
	t.Helper()
	parsed, err := adopt.Parse(conf)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mustMeshError(t *testing.T, err error, substr string) {
	t.Helper()
	var me *mesh.Error
	if !errors.As(err, &me) {
		t.Fatalf("expected *mesh.Error, got %T: %v", err, err)
	}
	if !strings.Contains(me.Msg, substr) {
		t.Fatalf("error %q must contain %q", me.Msg, substr)
	}
}

// writeState mirrors the Python test helper: parse conf, write env+db into
// a temp config/, return the temp root for render.RenderAll.
func writeState(t *testing.T, confText string) string {
	t.Helper()
	if confText == "" {
		confText = mustReadGolden(t)
	}
	parsed := mustParse(t, confText)
	envPairs, records := adopt.BuildState(
		parsed, "192.0.2.10", 443, "awg",
		"amneziavpn/amneziawg-go:latest", "1.1.1.1",
		"1.0.0.1", true, serverPubD44, "2026-10-22T00:00:00Z")
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	var envText strings.Builder
	for _, kv := range envPairs {
		envText.WriteString(kv[0] + "=" + kv[1] + "\n")
	}
	if err := os.WriteFile(filepath.Join(cfg, "server.env"), []byte(envText.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := peers.WithLock(filepath.Join(cfg, "peers.db"), func(tx *peers.Tx) error {
		for _, rec := range records {
			tx.Add(rec)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustReadGolden(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(goldenConf)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRoundtripAdoptRenderMatchesGoldenByteForByte(t *testing.T) {
	// golden/awg0.conf (output of the original render.py) -> adopt parser ->
	// server.env + peers.db -> render must reproduce golden exactly.
	root := writeState(t, "")
	if _, err := render.RenderAll(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"awg0.conf", "dnsmasq.conf", "start.sh"} {
		produced, err := os.ReadFile(filepath.Join(root, "config", name))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(fixturesDir, "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(produced, expected) {
			t.Fatalf("%s diverged after adopt roundtrip\n--- produced ---\n%s\n--- golden ---\n%s",
				name, produced, expected)
		}
	}
}

func TestParseUnknownInterfaceKeyAborts(t *testing.T) {
	_, err := adopt.Parse(strings.Replace(baseConf, "Jc = 5", "Jc = 5\nFoo = 1", 1))
	mustMeshError(t, err, "Foo")
}

func TestParseAddressNotFirstHostAborts(t *testing.T) {
	_, err := adopt.Parse(strings.Replace(baseConf,
		"Address = 100.64.0.1/24", "Address = 100.64.0.5/24", 1))
	mustMeshError(t, err, "first host")
}

func TestParsePeerMissingPSKAborts(t *testing.T) {
	_, err := adopt.Parse(strings.Replace(baseConf,
		"PresharedKey = "+psk+"\n", "", 1))
	mustMeshError(t, err, "PresharedKey")
}

func TestPeerWithoutCommentGetsGeneratedName(t *testing.T) {
	conf := strings.Replace(baseConf,
		"# test-peer (added 2026-10-20T12:00:00Z)\n", "", 1)
	parsed := mustParse(t, conf)
	want := "peer-" + strings.ToLower(pub[:6])
	if parsed.Peers[0].Name != want {
		t.Fatalf("name = %q, want %q", parsed.Peers[0].Name, want)
	}
	if !hasWarning(parsed.Warnings, "generated") {
		t.Fatalf("expected a 'generated' warning: %v", parsed.Warnings)
	}
}

func TestPeerNameCollisionGetsSuffix(t *testing.T) {
	extra := `[Peer]
# test-peer
PublicKey = EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.3/32
`
	parsed := mustParse(t, baseConf+extra)
	names := []string{parsed.Peers[0].Name, parsed.Peers[1].Name}
	if names[0] != "test-peer" || names[1] != "test-peer-2" {
		t.Fatalf("names = %v", names)
	}
}

func TestBuildStateCreatedFallback(t *testing.T) {
	conf := strings.Replace(baseConf, " (added 2026-10-20T12:00:00Z)", "", 1)
	parsed := mustParse(t, conf)
	_, records := adopt.BuildState(
		parsed, "h", 443, "awg", "img", "1.1.1.1", "", false,
		serverPubD44, "2026-10-23T00:00:00Z")
	if records[0].Created != "2026-10-23T00:00:00Z" {
		t.Fatalf("created = %q", records[0].Created)
	}
}

// --- real-world shapes found on a hand-maintained production conf -----------

func TestGeneratedNamesFromBase64PubkeysAreValid(t *testing.T) {
	// Hand-maintained confs have no name comments; pubkey slices contain
	// base64 chars (uppercase, /, +) and must sanitize to valid peer names.
	conf := strings.Replace(baseConf,
		"# test-peer (added 2026-10-20T12:00:00Z)\n", "", 1)
	parsed := mustParse(t, conf)
	for _, peer := range parsed.Peers {
		if !peers.ValidName(peer.Name) {
			t.Fatalf("invalid generated name %q", peer.Name)
		}
	}
}

func TestNonAddedParentheticalIsStripped(t *testing.T) {
	conf := strings.Replace(baseConf,
		"# test-peer (added 2026-10-20T12:00:00Z)",
		"# macbook-2 (key rotated 2026-10-27, was 1qtP0Bz2 without PSK)", 1)
	parsed := mustParse(t, conf)
	if parsed.Peers[0].Name != "macbook-2" {
		t.Fatalf("name = %q", parsed.Peers[0].Name)
	}
	if parsed.Peers[0].Created != "" {
		t.Fatalf("created = %q, want empty", parsed.Peers[0].Created)
	}
}

func TestOverlongCommentNameIsTruncatedToValid(t *testing.T) {
	longNote := strings.Repeat("some very long hand written comment about this device ", 3)
	conf := strings.Replace(baseConf,
		"# test-peer (added 2026-10-20T12:00:00Z)", "# "+longNote, 1)
	parsed := mustParse(t, conf)
	name := parsed.Peers[0].Name
	if len(name) > 32 || !peers.ValidName(name) {
		t.Fatalf("name %q must be a valid DNS label ≤32 chars", name)
	}
}

func TestProdShapedConfProducesValidState(t *testing.T) {
	// Peer mix from the real server: unnamed peers with base64 keys, one
	// long hand-written note. adopt -> render must succeed end to end.
	conf := `[Interface]
PrivateKey = ` + priv + `
Address = 100.64.0.1/24
ListenPort = 41480
MTU = 1280
Jc = 5
Jmin = 14
Jmax = 52
S1 = 37
S2 = 34
S3 = 200
S4 = 128
H1 = 1641846015
H2 = 1919277141
H3 = 1925319215
H4 = 4119105387
HeaderProtectionKey = ` + psk + `
ContentPaddingAddition = 2-10
RekeyAfterTime = 120-180
RekeyTimeout = 5-10
RejectAfterTime = 540-600
KeepaliveTimeout = 8-12
MaxHandshakeAttempts = 80-100
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = D38w6bG59pL7AdUCxxxxxxxxxxxxxxxxxxxxxxxxxxx=
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.1/32
PersistentKeepalive = 25

[Peer]
PublicKey = 0idOZrIUtbQTnDIPyyyyyyyyyyyyyyyyyyyyyyyyyyy=
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.2/32

[Peer]
PublicKey = n117z/3AeWhuaMNNzzzzzzzzzzzzzzzzzzzzzzzzzzz=
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.3/32

[Peer]
# macbook-2 (key rotated 2026-10-27, was 1qtP0Bz2 without PSK)
PublicKey = 2Q41d93FiHUxxiULwwwwwwwwwwwwwwwwwwwwwwwwwww=
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.4/32

[Peer]
# macbook-3
PublicKey = 3MiTsQx5h+JxUnmmvvvvvvvvvvvvvvvvvvvvvvvvvvvv=
PresharedKey = ` + psk + `
AllowedIPs = 100.64.0.5/32
`
	root := writeState(t, conf)
	if _, err := render.RenderAll(root); err != nil { // must not raise
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "awg0.conf")); err != nil {
		t.Fatal(err)
	}
	dbPeers, err := peers.Load(filepath.Join(root, "config", "peers.db"))
	if err != nil {
		t.Fatal(err)
	}
	ips := map[string]bool{}
	for _, p := range dbPeers {
		if !peers.ValidName(p.Name) {
			t.Fatalf("invalid name %q", p.Name)
		}
		ips[p.IP] = true
	}
	for i := 1; i <= 5; i++ {
		if !ips[fmt.Sprintf("100.64.0.%d", i)] {
			t.Fatalf("missing ip 100.64.0.%d in %v", i, ips)
		}
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
