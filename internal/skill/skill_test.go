package skill

import (
	"strings"
	"testing"
)

func TestLoadHasFrontmatterAndBody(t *testing.T) {
	md := Load()
	if !strings.HasPrefix(md, "---\nname: awg-mesh\n") {
		t.Fatalf("SKILL.md frontmatter mismatch:\n%.80q", md)
	}
	for _, want := range []string{
		"description:", "## Install the CLI", "## Troubleshooting decision tree",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("SKILL.md missing %q", want)
		}
	}
}
