package envfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCommentsBlankLinesAndSpacing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.env")
	text := "# comment\n\nKEY1=value1\nKEY2 = spaced \nKEY3=a=b\n"
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if env["KEY1"] != "value1" || env["KEY2"] != "spaced" || env["KEY3"] != "a=b" {
		t.Fatalf("unexpected parse result: %v", env)
	}
	if _, ok := env["comment"]; ok {
		t.Fatal("comment must not be parsed as a key")
	}
}

func TestMissingFileRaisesMeshError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.env")
	if _, err := Load(p); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}

func TestValuesAreDataNeverExecuted(t *testing.T) {
	// The old bash CLI sourced server.env; a tampered file could run code.
	dir := t.TempDir()
	p := filepath.Join(dir, "server.env")
	marker := filepath.Join(dir, "pwned")
	text := "EVIL=$(touch " + marker + ")\nOTHER=1\n"
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if env["EVIL"] != "$(touch "+marker+")" { // stored verbatim
		t.Fatalf("EVIL not stored verbatim: %q", env["EVIL"])
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker must not exist: value was executed")
	}
}

func TestGetReturnsDefaultForMissingOrEmpty(t *testing.T) {
	if got := Get(map[string]string{}, "MISSING", "dflt"); got != "dflt" {
		t.Fatalf("missing: got %q", got)
	}
	if got := Get(map[string]string{"K": ""}, "K", "dflt"); got != "dflt" {
		t.Fatalf("empty: got %q", got)
	}
	if got := Get(map[string]string{"K": "v"}, "K", "dflt"); got != "v" {
		t.Fatalf("set: got %q", got)
	}
}
