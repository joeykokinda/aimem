package activity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/config"
)

// The extractor decides what crosses out of a private folder, so these cases are the
// privacy boundary expressed as a test. A regression here leaks the journal.
func TestLineEntry(t *testing.T) {
	shared := map[string]bool{"Alpha": true, "Beta": true}

	cases := []struct {
		name    string
		line    string
		want    string // emitted text, "" when nothing is emitted
		target  string
		skipped bool
		secret  bool
	}{{
		name:   "line linking a shared note is emitted whole",
		line:   "- worked on [[Alpha]], deposit sweep works now",
		want:   "worked on [[Alpha]], deposit sweep works now",
		target: "Alpha",
	}, {
		name: "plain journal prose never leaves",
		line: "- dentist at 3pm, felt awful afterwards",
	}, {
		name:    "link to an unknown note is held back",
		line:    "- talked to [[Private Note]] about the results",
		skipped: true,
	}, {
		name:    "private tag opts a line out",
		line:    "- [[Beta]] billing is out of control #private",
		skipped: true,
	}, {
		name:    "nolog is accepted as an alias",
		line:    "- [[Beta]] thinking about giving up #nolog",
		skipped: true,
	}, {
		name:   "obsidian comments are stripped, not skipped",
		line:   "- [[Alpha]] sweep works %%and I am stressed about money%%",
		want:   "[[Alpha]] sweep works",
		target: "Alpha",
	}, {
		name:   "a tag that merely starts with private does not opt out",
		line:   "- [[Alpha]] see #privateKeyRotation",
		want:   "[[Alpha]] see #privateKeyRotation",
		target: "Alpha",
	}, {
		name:   "secrets are blocked even on an otherwise valid line",
		line:   "- [[Alpha]] deploy key AKIAIOSFODNN7EXAMPLE",
		secret: true,
	}, {
		name:   "first shared link wins when several are present",
		line:   "- [[Private Note]] then [[Beta]] then [[Alpha]]",
		want:   "[[Private Note]] then [[Beta]] then [[Alpha]]",
		target: "Beta",
	}}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			entry, skipped, secret := lineEntry(test.line, "2026-09-27", shared)

			if secret != test.secret {
				t.Fatalf("secret = %v, want %v", secret, test.secret)
			}
			if skipped != test.skipped {
				t.Fatalf("skipped = %v, want %v", skipped, test.skipped)
			}
			if test.want == "" {
				if entry != nil {
					t.Fatalf("emitted %q, expected nothing to cross the boundary", entry.Text)
				}
				return
			}
			if entry == nil {
				t.Fatalf("emitted nothing, want %q", test.want)
			}
			if entry.Text != test.want {
				t.Errorf("text = %q, want %q", entry.Text, test.want)
			}
			if entry.Target != test.target {
				t.Errorf("target = %q, want %q", entry.Target, test.target)
			}
		})
	}
}

// TestJournalMustBePrivate asserts the bridge refuses a journal folder outside the
// private tier. The bridge narrows private access; it must never be able to grant it.
func TestJournalMustBePrivate(t *testing.T) {
	root := t.TempDir()
	settings := config.Default(root)
	settings.Journal = settings.Shared[0]
	if _, err := Extract(settings, map[string]bool{}); err == nil {
		t.Fatal("Extract accepted a journal folder outside the private tier")
	}
}

// TestExtract exercises the real walk against a synthetic journal, so the folder
// traversal and date handling are covered without reading anyone's actual diary.
func TestExtract(t *testing.T) {
	root := t.TempDir()
	settings := config.Default(root)
	daily := filepath.Join(root, settings.Journal)
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(root, settings.Meta)
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(daily, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-09-27.md", "---\ntype: journal\n---\n"+
		"- worked on [[Alpha]], deposit sweep works now\n"+
		"- felt rough all morning\n"+
		"- [[Beta]] bill still running #private\n")
	write("2026-09-21.md", "---\ntype: journal\n---\n"+
		"- [[Alpha]] started the refund path\n")

	result, err := Extract(settings, map[string]bool{"Alpha": true, "Beta": true})
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesRead != 2 {
		t.Errorf("FilesRead = %d, want 2", result.FilesRead)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(result.Entries), result.Entries)
	}
	if result.LinesSkipped != 1 {
		t.Errorf("LinesSkipped = %d, want 1 (the #private line)", result.LinesSkipped)
	}

	// Newest first, and the date comes from the filename.
	if result.Entries[0].Date != "2026-09-27" {
		t.Errorf("first entry date = %q, want 2026-09-27", result.Entries[0].Date)
	}
	for _, entry := range result.Entries {
		if strings.Contains(entry.Text, "felt rough") || strings.Contains(entry.Text, "bill still running") {
			t.Fatalf("private text crossed the boundary: %q", entry.Text)
		}
	}

	if _, err := Render(settings, result, 12); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(filepath.Join(meta, "Activity.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "## [[Alpha]]") {
		t.Error("rendered timeline is missing the Alpha heading")
	}
	if strings.Contains(string(rendered), "felt rough") {
		t.Error("rendered timeline leaked a line with no wikilink")
	}
	if latest := LastTouched(result)["Alpha"]; latest != "2026-09-27" {
		t.Errorf("LastTouched = %q, want 2026-09-27", latest)
	}
}
