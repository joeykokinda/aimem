// Package testvault builds throwaway vaults for tests.
//
// Every test that touches the privacy boundary needs the same thing: a vault with real
// folders in every tier and known content in each, so a test can assert that private
// content is absent from output rather than merely that shared content is present.
// Those are different claims, and only the first one is a security property.
package testvault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/config"
)

// Canary is planted in every private and locked file. Any test output containing it
// means private content escaped, wherever it escaped from.
const Canary = "CANARY-PRIVATE-DO-NOT-LEAK-9f3a2b"

// Build creates a vault with the standard tiers, a note in each, and canaries planted
// throughout the private and locked folders. It returns the loaded config.
func Build(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()

	settings := config.Default(root)
	settings.Name = "Testvault"
	settings.SyncEnabled = false
	if err := settings.Write(); err != nil {
		t.Fatalf("write config: %v", err)
	}

	Write(t, root, "Projects/Alpha.md", `---
type: project
status: active
company: Acme
repo: /nonexistent/alpha
tags: [go, indexing]
---
# Alpha

## Goal
Alpha is the shared project used across tests.

Links to [[Beta]].
`)
	Write(t, root, "Projects/Beta.md", `---
type: project
status: paused
tags: [rust]
---
# Beta

Beta is paused work. See [[Alpha]].
`)
	Write(t, root, "Companies/Acme.md", `---
type: company
status: active
---
# Acme

Holding company for [[Alpha]].
`)
	Write(t, root, "Research/Indexing.md", `---
type: research
status: evergreen
---
# Indexing

Notes on retrieval. Mentions [[Alpha]].
`)
	// A shared-folder note that opts out. It must be absent from every output path:
	// this is the case a generic filesystem server gets wrong.
	Write(t, root, "Projects/Secretive.md", `---
type: project
status: active
scope: private
---
# Secretive

`+Canary+`
`)
	Write(t, root, "Meta/README.md", `---
type: reference
status: evergreen
---
# Meta

Holds generated files. [[Alpha]] [[Beta]] [[Acme]] [[Indexing]]
`)

	// Private tier. Every file carries a canary.
	Write(t, root, "Inbox/Scratch.md", "# Scratch\n\n"+Canary+"\n")
	Write(t, root, "Personal/Health.md", "# Health\n\n"+Canary+"\n")
	Write(t, root, "Daily/2026-09-28.md", `# 2026-09-28

- worked on [[Alpha]], the indexer is faster now
- `+Canary+` with no link at all
- [[Alpha]] and also `+Canary+` #private
- thinking about [[Beta]] %%`+Canary+`%%
- [[Alpha]] deploy key AKIAIOSFODNN7EXAMPLE
`)
	// Locked tier, as if mounted.
	Write(t, root, "Locked/Keys.md", "# Keys\n\n"+Canary+"\n")
	Write(t, root, ".gitignore", "Locked/\n")

	loaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	loaded.SyncEnabled = false
	return loaded
}

// Write creates a file under the vault, making parent directories as needed.
func Write(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

// AssertNoCanary fails the test if private content appears in the given text.
func AssertNoCanary(t *testing.T, label, text string) {
	t.Helper()
	if strings.Contains(text, Canary) {
		t.Errorf("PRIVACY BOUNDARY VIOLATION: %s contains private content", label)
	}
}
