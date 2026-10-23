package peers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func peer(name, ip string) Peer {
	return Peer{Name: name, IP: ip, Pub: "p", PSK: "k", Created: "t"}
}

func TestValidNameDNSLabel(t *testing.T) {
	valid := []string{"macbook-3", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	invalid := []string{"Hub", "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bad_name", "ünïcode"}
	for _, name := range valid {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
	}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
	}
}

func TestNextFreeIPReservesHubAndSkipsUsed(t *testing.T) {
	ip, ok, err := NextFreeIP("100.64.0.0/24", nil)
	if err != nil || !ok || ip != "100.64.0.2" { // .1 is hub
		t.Fatalf("empty db: ip=%q ok=%v err=%v", ip, ok, err)
	}
	used := []Peer{peer("a", "100.64.0.2"), peer("b", "100.64.0.3")}
	ip, ok, err = NextFreeIP("100.64.0.0/24", used)
	if err != nil || !ok || ip != "100.64.0.4" {
		t.Fatalf("used .2,.3: ip=%q ok=%v err=%v", ip, ok, err)
	}
}

func TestNextFreeIPExhaustion(t *testing.T) {
	// /30: only .1 (hub) and .2 are hosts; .2 is taken.
	used := []Peer{peer("a", "100.64.0.2")}
	ip, ok, err := NextFreeIP("100.64.0.0/30", used)
	if err != nil || ok || ip != "" {
		t.Fatalf("exhausted: ip=%q ok=%v err=%v", ip, ok, err)
	}
}

func TestAddRemoveRoundtrip(t *testing.T) {
	db := filepath.Join(t.TempDir(), "peers.db")
	if err := WithLock(db, func(tx *Tx) error {
		tx.Add(peer("a", "100.64.0.2"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(db)
	if err != nil || len(loaded) != 1 || loaded[0].Name != "a" {
		t.Fatalf("after add: %v, %v", loaded, err)
	}
	err = WithLock(db, func(tx *Tx) error {
		if tx.Find("a") == nil {
			t.Fatal("a must be found")
		}
		if !tx.Remove("a") {
			t.Fatal("first remove must return true")
		}
		if tx.Remove("a") { // already gone, not dirty
			t.Fatal("second remove must return false")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(db)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("after remove: %v, %v", loaded, err)
	}
}

func TestDBIsCreatedOnFirstWrite(t *testing.T) {
	db := filepath.Join(t.TempDir(), "sub", "peers.db")
	if err := WithLock(db, func(tx *Tx) error {
		tx.Add(peer("a", "100.64.0.2"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(db)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("created in subdir: %v, %v", loaded, err)
	}
}

func TestRenamePeer(t *testing.T) {
	db := filepath.Join(t.TempDir(), "peers.db")
	if err := WithLock(db, func(tx *Tx) error {
		tx.Add(peer("a", "100.64.0.2"))
		tx.Add(peer("b", "100.64.0.3"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(db, func(tx *Tx) error {
		if !tx.Rename("a", "phone") {
			t.Fatal("rename a→phone must succeed")
		}
		if tx.Rename("a", "x") {
			t.Fatal("rename from gone name must fail")
		}
		if tx.Rename("phone", "b") {
			t.Fatal("rename to taken name must fail")
		}
		if tx.Rename("nope", "x") {
			t.Fatal("rename of missing peer must fail")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Name != "phone" || loaded[1].Name != "b" {
		t.Fatalf("after rename: %v", loaded)
	}
	if loaded[0].IP != "100.64.0.2" { // keys/IPs untouched
		t.Fatalf("rename changed IP: %v", loaded[0])
	}
}

func TestParseRejectsInvalidName(t *testing.T) {
	if _, err := Parse("Bad\t100.64.0.2\tp\tk\tt\n"); err == nil {
		t.Fatal("invalid name must fail parsing")
	}
}

func TestConcurrentAddsSerializeWithoutTornLines(t *testing.T) {
	db := filepath.Join(t.TempDir(), "peers.db")
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- WithLock(db, func(tx *Tx) error {
				ip, ok, err := NextFreeIP("100.64.0.0/24", tx.Peers)
				if err != nil {
					return err
				}
				if !ok {
					return ErrNoFreeIP
				}
				tx.Add(Peer{Name: fmt.Sprintf("w%d", i), IP: ip, Pub: "p", PSK: "k", Created: "t"})
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("worker error: %v", err)
		}
	}
	plist, err := Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(plist) != 8 {
		t.Fatalf("want 8 peers, got %d", len(plist))
	}
	seen := map[string]bool{}
	for _, p := range plist {
		if seen[p.IP] {
			t.Fatalf("duplicate IP %q: flock + in-lock allocation broken", p.IP)
		}
		seen[p.IP] = true
	}
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("want 8 lines, got %d", len(lines))
	}
	for _, line := range lines {
		if strings.Count(line, "\t") != 4 {
			t.Fatalf("torn line: %q", line)
		}
	}
}

func TestParseSkipsShortLinesAndDropsExtras(t *testing.T) {
	// 4-field line skipped, 6-field line truncated to 5.
	plist, err := Parse("a\t100.64.0.2\tp\tk\n" +
		"b\t100.64.0.3\tp\tk\tt\tEXTRA\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(plist) != 1 || plist[0].Name != "b" || plist[0].Created != "t" {
		t.Fatalf("unexpected: %v", plist)
	}
}
