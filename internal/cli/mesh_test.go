package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexpozdnyakof/amnezia-mesh-go/internal/profile"
)

func issue(t *testing.T, root, name, ip, pub, psk string) {
	t.Helper()
	_, err := profile.GenerateProfiles(filepath.Join(root, "config", "server.env"), name, ip,
		name+"-PRIV", pub, psk, filepath.Join(root, "config", "peers", name))
	if err != nil {
		t.Fatal(err)
	}
}

func p2p(root, name string) string {
	return filepath.Join(root, "config", "peers", name+"-mesh-p2p.conf")
}

func TestMeshLinkEndpointConfigFlow(t *testing.T) {
	root, c, _ := opsMesh(t, "")
	issue(t, root, "laptop-test", "100.64.0.3", "MonOGpsaD1LBdUv02MRnrJBbuyszcrJBtuFxkPDCvH4=", "447vmk7GCKPkQXBH9qqbKJBl7YxHukFEFyAv6K0UcCs=")
	issue(t, root, "hub-test", "100.64.0.2", "mlHSVfgGC/c+oryh1olYX53TBqYPdjL8ARbppAuJd38=", "D5LHfFlansvHmRqCIn9tdQF1+phuU9arLKQMxpwypi8=")
	c.JSON = true
	var out strings.Builder
	c.Stdout = &out
	if code := c.Run([]string{"mesh", "link", "add", "laptop-test", "hub-test"}); code != 0 {
		t.Fatalf("link add: %d %s", code, out.String())
	}
	b, _ := os.ReadFile(p2p(root, "laptop-test"))
	if strings.Count(string(b), "[Peer]") != 1 {
		t.Fatal("no endpoint yet: profile must be hub-only")
	}
	out.Reset()
	if code := c.Run([]string{"mesh", "endpoint", "set", "hub-test", "203.0.113.50:51820", "--note", "pf"}); code != 0 {
		t.Fatalf("endpoint set: %d %s", code, out.String())
	}
	var res struct {
		Affected []string `json:"affected"`
	}
	json.Unmarshal([]byte(out.String()), &res)
	if len(res.Affected) != 1 || res.Affected[0] != "laptop-test" {
		t.Fatalf("affected: %s", out.String())
	}
	b, _ = os.ReadFile(p2p(root, "laptop-test"))
	conf := string(b)
	if strings.Count(conf, "[Peer]") != 2 || !strings.Contains(conf, "AllowedIPs = 100.64.0.2/32") ||
		!strings.Contains(conf, "Endpoint = 203.0.113.50:51820") ||
		!strings.Contains(conf, "laptop-test-PRIV") || strings.Contains(conf, "hub-test-PRIV") {
		t.Fatalf("p2p conf:\n%s", conf)
	}
	out.Reset()
	c.Run([]string{"mesh", "status"})
	if !strings.Contains(out.String(), `"direct_capable": false`) || !strings.Contains(out.String(), `"missing": ["laptop-test"]`) {
		t.Fatalf("status: %s", out.String())
	}
}

func TestMeshConfigWithoutLinksFails(t *testing.T) {
	_, c, _ := opsMesh(t, "")
	c.JSON = true
	var out strings.Builder
	c.Stdout = &out
	if code := c.Run([]string{"mesh", "config", "laptop-test"}); code != 1 || !strings.Contains(out.String(), "no links") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestMeshRevokePrunesAndRenameRewrites(t *testing.T) {
	root, c, _ := opsMesh(t, "")
	issue(t, root, "laptop-test", "100.64.0.3", "MonOGpsaD1LBdUv02MRnrJBbuyszcrJBtuFxkPDCvH4=", "447vmk7GCKPkQXBH9qqbKJBl7YxHukFEFyAv6K0UcCs=")
	c.JSON = true
	c.Run([]string{"mesh", "link", "add", "laptop-test", "hub-test"})
	c.Run([]string{"peer", "rename", "hub-test", "nas"})
	links, _ := os.ReadFile(filepath.Join(root, "config", "mesh-links.json"))
	if !strings.Contains(string(links), `"b": "nas"`) {
		t.Fatalf("rename did not rewrite links: %s", links)
	}
	if code := c.Run([]string{"peer", "revoke", "nas"}); code != 0 {
		t.Fatal(code)
	}
	links, _ = os.ReadFile(filepath.Join(root, "config", "mesh-links.json"))
	if !strings.Contains(string(links), `"links": []`) {
		t.Fatalf("revoke did not prune links: %s", links)
	}
	if _, err := os.Stat(p2p(root, "laptop-test")); err == nil {
		t.Fatal("partner p2p files must go once no links remain")
	}
}

func TestMeshIsMutatingForRemoteSync(t *testing.T) {
	for argv, want := range map[string]bool{
		"mesh link add a b": true, "mesh link remove a b": true,
		"mesh endpoint set a 1.2.3.4:5": true, "mesh endpoint unset a": true,
		"mesh config a": true, "mesh status": false, "mesh link list": false,
		"mesh endpoint list": false,
	} {
		if got := isMutating(strings.Fields(argv)); got != want {
			t.Errorf("%s: %v", argv, got)
		}
	}
}
