package keygen

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

func parseLines(t *testing.T, lines []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("not a KEY=VALUE line: %q", line)
		}
		m[k] = v
	}
	return m
}

func TestGenObfuscationDeterministicWithSeed(t *testing.T) {
	t.Setenv("AWG_MESH_SEED", "42")
	a := GenObfuscation()
	b := GenObfuscation()
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("seeded obfuscation not deterministic:\n%v\n%v", a, b)
	}
}

func TestGenObfuscationRangesAndShape(t *testing.T) {
	t.Setenv("AWG_MESH_SEED", "7")
	m := parseLines(t, GenObfuscation())

	jc, _ := strconv.Atoi(m["OBF_JC"])
	if jc < 3 || jc > 7 {
		t.Fatalf("OBF_JC = %d, want 3..7", jc)
	}
	jmin, _ := strconv.Atoi(m["OBF_JMIN"])
	if jmin < 10 || jmin > 20 {
		t.Fatalf("OBF_JMIN = %d, want 10..20", jmin)
	}
	jmax, _ := strconv.Atoi(m["OBF_JMAX"])
	if jmax < jmin+30 || jmax > jmin+50 {
		t.Fatalf("OBF_JMAX = %d, want %d..%d", jmax, jmin+30, jmin+50)
	}

	s := map[int]bool{}
	for i := 1; i <= 4; i++ {
		v, _ := strconv.Atoi(m["OBF_S"+strconv.Itoa(i)])
		if v < 30 || v > 219 {
			t.Fatalf("OBF_S%d = %d, want 30..219", i, v)
		}
		if s[v] {
			t.Fatalf("OBF_S%d duplicates a previous S value", i)
		}
		s[v] = true
	}
	h := map[int64]bool{}
	for i := 1; i <= 4; i++ {
		v, _ := strconv.ParseInt(m["OBF_H"+strconv.Itoa(i)], 10, 64)
		if v < 1 || v > 1<<32-1 {
			t.Fatalf("OBF_H%d = %d, want 1..2^32-1", i, v)
		}
		if h[v] {
			t.Fatalf("OBF_H%d duplicates a previous H value", i)
		}
		h[v] = true
	}
	hpk, err := base64.StdEncoding.DecodeString(m["OBF_HEADERPROTECTIONKEY"])
	if err != nil || len(hpk) != 32 {
		t.Fatalf("OBF_HEADERPROTECTIONKEY: len=%d err=%v", len(hpk), err)
	}

	tail := []string{
		"OBF_CONTENTPADDINGADDITION=2-10",
		"OBF_REKEYAFTERTIME=120-180",
		"OBF_REKEYTIMEOUT=5-10",
		"OBF_REJECTAFTERTIME=540-600",
		"OBF_KEEPALIVETIMEOUT=8-12",
		"OBF_MAXHANDSHAKEATTEMPTS=80-100",
		"OBF_RANDOMTRAILERS=on",
		"OBF_DISABLECOOKIES=on",
	}
	lines := GenObfuscation()
	if strings.Join(lines[len(lines)-len(tail):], "\n") != strings.Join(tail, "\n") {
		t.Fatalf("constant tail diverged: %v", lines[len(lines)-len(tail):])
	}
}

func TestGenObfuscationCryptoIsRandom(t *testing.T) {
	a := GenObfuscation()
	b := GenObfuscation()
	if strings.Join(a, "\n") == strings.Join(b, "\n") {
		t.Fatal("unseeded obfuscation must differ between calls")
	}
}
