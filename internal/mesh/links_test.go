package mesh

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const now = "2026-11-19T12:00:00Z"

var names = map[string]bool{"laptop": true, "nas": true, "phone": true}

func root(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	if err := os.MkdirAll(filepath.Join(r, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("want error containing %q, got %v", sub, err)
	}
}

func writeObserved(t *testing.T, r, updated, peers string) {
	t.Helper()
	body := `{"version":1,"updated":"` + updated + `","peers":` + peers + `}`
	if err := os.WriteFile(filepath.Join(r, "config", ObservedFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLinkAddUnorderedUnique(t *testing.T) {
	r := root(t)
	if err := AddLink(r, "laptop", "nas", names, now); err != nil {
		t.Fatal(err)
	}
	mustErr(t, AddLink(r, "nas", "laptop", names, now), "already linked")
	mustErr(t, AddLink(r, "nas", "nas", names, now), "itself")
	mustErr(t, AddLink(r, "nas", "ghost", names, now), "no such peer: ghost")
	p, _ := Partners(r, "nas")
	if !reflect.DeepEqual(p, []string{"laptop"}) {
		t.Fatalf("partners: %v", p)
	}
	mustErr(t, RemoveLink(r, "x", "y"), "not linked")
	if err := RemoveLink(r, "nas", "laptop"); err != nil {
		t.Fatal(err)
	}
}

// Byte shape of the files is the cross-implementation contract
// (json.dump(indent=2) + newline).
func TestFilesMatchPythonIndentFormat(t *testing.T) {
	r := root(t)
	if err := AddLink(r, "laptop", "nas", names, now); err != nil {
		t.Fatal(err)
	}
	if err := SetStatic(r, "nas", "203.0.113.50:51820", "port-forward", names); err != nil {
		t.Fatal(err)
	}
	links, _ := os.ReadFile(filepath.Join(r, "config", LinksFile))
	wantLinks := "{\n  \"version\": 1,\n  \"links\": [\n    {\n      \"a\": \"laptop\",\n      \"b\": \"nas\",\n      \"created\": \"2026-11-19T12:00:00Z\"\n    }\n  ]\n}\n"
	if string(links) != wantLinks {
		t.Fatalf("links file:\n%q\nwant\n%q", links, wantLinks)
	}
	st, _ := os.ReadFile(filepath.Join(r, "config", StaticFile))
	wantSt := "{\n  \"version\": 1,\n  \"endpoints\": {\n    \"nas\": {\n      \"endpoint\": \"203.0.113.50:51820\",\n      \"note\": \"port-forward\"\n    }\n  }\n}\n"
	if string(st) != wantSt {
		t.Fatalf("static file:\n%q\nwant\n%q", st, wantSt)
	}
	// empty containers render as [] / {}
	if err := RemoveLink(r, "laptop", "nas"); err != nil {
		t.Fatal(err)
	}
	links, _ = os.ReadFile(filepath.Join(r, "config", LinksFile))
	if string(links) != "{\n  \"version\": 1,\n  \"links\": []\n}\n" {
		t.Fatalf("empty links: %q", links)
	}
	if _, err := os.Stat(filepath.Join(r, "config", LinksFile+".tmp")); err == nil {
		t.Fatal("tmp file left behind")
	}
}

func TestUnsupportedVersion(t *testing.T) {
	r := root(t)
	os.WriteFile(filepath.Join(r, "config", LinksFile), []byte(`{"version": 2}`), 0o644)
	_, err := LoadLinks(r)
	mustErr(t, err, "unsupported")
}

func TestParseEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.50:51820":   "203.0.113.50:51820",
		"nas.example.org:0443": "nas.example.org:443",
	} {
		got, err := ParseEndpoint(in)
		if err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"203.0.113.50", ":51820", "host:", "host:0", "host:70000", "ho st:1", "-bad:1", "host:12a", "host:99999999999999999999"} {
		if _, err := ParseEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	_, err := ParseEndpoint("ho st:1")
	mustErr(t, err, "invalid endpoint host: 'ho st'")
}

func TestStaticSetUnsetKeepsPosition(t *testing.T) {
	r := root(t)
	SetStatic(r, "nas", "1.1.1.1:1", "", names)
	SetStatic(r, "laptop", "2.2.2.2:2", "", names)
	SetStatic(r, "nas", "3.3.3.3:3", "n", names)
	eps, _ := LoadStatic(r)
	if eps[0].Name != "nas" || eps[0].Endpoint != "3.3.3.3:3" || eps[1].Name != "laptop" {
		t.Fatalf("order: %+v", eps)
	}
	mustErr(t, SetStatic(r, "ghost", "1.2.3.4:5", "", names), "no such peer")
	if err := UnsetStatic(r, "nas"); err != nil {
		t.Fatal(err)
	}
	mustErr(t, UnsetStatic(r, "nas"), "no static endpoint")
}

func TestObservedFreshness(t *testing.T) {
	r := root(t)
	writeObserved(t, r, "2026-11-19T11:59:00Z", `[
	 {"name":"laptop","endpoint":"1.1.1.1:1","handshake_age":30},
	 {"name":"nas","endpoint":"2.2.2.2:2","handshake_age":9999},
	 {"name":"phone","endpoint":"","handshake_age":5},
	 {"name":"old","endpoint":"3.3.3.3:3","handshake_age":null}]`)
	got, err := LoadObserved(r, now)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"laptop": "1.1.1.1:1"}) {
		t.Fatalf("%v %v", got, err)
	}
	writeObserved(t, r, "2026-11-19T11:00:00Z", `[{"name":"laptop","endpoint":"1.1.1.1:1","handshake_age":1}]`)
	if got, _ := LoadObserved(r, now); len(got) != 0 {
		t.Fatalf("stale snapshot used: %v", got)
	}
	os.Remove(filepath.Join(r, "config", ObservedFile))
	if got, _ := LoadObserved(r, now); len(got) != 0 {
		t.Fatal("missing file")
	}
}

func TestStaticBeatsObserved(t *testing.T) {
	st := []Static{{"nas", "9.9.9.9:9", ""}}
	ob := map[string]string{"nas": "1.1.1.1:1", "laptop": "2.2.2.2:2"}
	if ep, src, ok := ResolveEndpoint("nas", st, ob); !ok || ep != "9.9.9.9:9" || src != "static" {
		t.Fatal("nas")
	}
	if ep, src, ok := ResolveEndpoint("laptop", st, ob); !ok || ep != "2.2.2.2:2" || src != "observed" {
		t.Fatal("laptop")
	}
	if _, _, ok := ResolveEndpoint("phone", st, ob); ok {
		t.Fatal("phone")
	}
}

func TestPruneAndRename(t *testing.T) {
	r := root(t)
	AddLink(r, "laptop", "nas", names, now)
	AddLink(r, "nas", "phone", names, now)
	SetStatic(r, "nas", "1.2.3.4:5", "", names)
	if err := RenamePeers(r, map[string]string{"laptop": "nas", "nas": "laptop"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Partners(r, "laptop"); !reflect.DeepEqual(p, []string{"nas", "phone"}) {
		t.Fatalf("swap: %v", p)
	}
	if eps, _ := LoadStatic(r); len(eps) != 1 || eps[0].Name != "laptop" {
		t.Fatalf("static swap: %+v", eps)
	}
	former, err := PrunePeer(r, "laptop")
	if err != nil || !reflect.DeepEqual(former, []string{"nas", "phone"}) {
		t.Fatalf("%v %v", former, err)
	}
	if l, _ := LoadLinks(r); len(l) != 0 {
		t.Fatal("links remain")
	}
	if eps, _ := LoadStatic(r); len(eps) != 0 {
		t.Fatal("static remains")
	}
}

func TestReadIdentity(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "x-mesh.conf")
	os.WriteFile(p, []byte("[Interface]\nPrivateKey = PRIV\n\n[Peer]\nPresharedKey = PSK\n"), 0o644)
	priv, psk, err := ReadIdentity(p)
	if err != nil || priv != "PRIV" || psk != "PSK" {
		t.Fatalf("%q %q %v", priv, psk, err)
	}
	_, _, err = ReadIdentity(filepath.Join(d, "nope.conf"))
	mustErr(t, err, "peer rotate")
}

func TestDirectPeersSplit(t *testing.T) {
	r := root(t)
	AddLink(r, "laptop", "nas", names, now)
	AddLink(r, "laptop", "phone", names, now)
	SetStatic(r, "nas", "203.0.113.50:51820", "", names)
	writeObserved(t, r, "2026-11-19T11:59:30Z", `[{"name":"phone","endpoint":"85.1.2.3:4000","handshake_age":20}]`)
	pl := []PeerInfo{{"laptop", "100.64.0.3", "LAPPUB"}, {"nas", "100.64.0.5", "NASPUB"}, {"phone", "100.64.0.4", "PHPUB"}}
	managed, static, skipped, err := DirectPeers(r, "laptop", pl, now)
	if err != nil || len(managed) != 2 || len(static) != 1 || static[0].Name != "nas" || len(skipped) != 0 {
		t.Fatalf("%+v %+v %v %v", managed, static, skipped, err)
	}
	r2 := root(t)
	AddLink(r2, "laptop", "nas", names, now)
	managed, static, skipped, _ = DirectPeers(r2, "laptop", pl, now)
	if len(managed) != 0 || len(static) != 0 || !reflect.DeepEqual(skipped, []string{"nas"}) {
		t.Fatal("endpointless partner must be skipped")
	}
}
