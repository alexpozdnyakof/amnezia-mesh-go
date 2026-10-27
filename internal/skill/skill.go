// Package skill carries the awg-mesh operator guide (SKILL.md) inside the
// binary: the CLI prints it (`awg-mesh skill`) and can install it where
// coding agents discover skills (`awg-mesh skill --install`).
package skill

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var skillMD string

// Load returns the bundled SKILL.md.
func Load() string { return skillMD }

// Install copies SKILL.md to ~/.agents/skills/awg-mesh/ and returns the
// destination path.
func Install() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".agents", "skills", "awg-mesh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(skillMD), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
