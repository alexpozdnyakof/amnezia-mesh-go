// Package envfile is a safe parser for config/server.env.
//
// Values are read as DATA, never sourced or executed — the old bash CLI did
// `. server.env`, which executed arbitrary code if the file was tampered with.
package envfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// ErrNotInitialized reports a missing server.env (the mesh was not initialized).
var ErrNotInitialized = errors.New("not initialized")

// Load parses an env file into a map. A missing file yields
// ErrNotInitialized, matching the Python MeshError message.
func Load(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s missing (run: awg-mesh init)", ErrNotInitialized, path)
		}
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		env[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return env, nil
}

// Get returns env[key], or def when the value is missing or empty.
// Last non-empty value wins, like the old `grep ^KEY= | tail -1`.
func Get(env map[string]string, key, def string) string {
	if v := env[key]; v != "" {
		return v
	}
	return def
}
