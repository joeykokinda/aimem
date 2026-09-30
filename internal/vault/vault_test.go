package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/config"
)

func TestWikiLinksIgnoresCodeAndNormalizesTargets(t *testing.T) {
	body := "[[Project|label]] [[Research#section]] `[[Inline]]`\n```md\n[[Fence]]\n```\n[[Project]]"
	want := []string{"Project", "Research"}
	if got := WikiLinks(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("WikiLinks() = %#v, want %#v", got, want)
	}
}

// testConfig builds a minimal vault contract without importing the testvault helper,
// which would be an import cycle.
func testConfig(root string) *config.Config {
	settings := config.Default(root)
	settings.Root = root
	return settings
}

func TestCollectRefusesPrivateFolders(t *testing.T) {
	root := t.TempDir()
	settings := testConfig(root)
	private := filepath.Join(root, settings.Private[0])
	if err := os.MkdirAll(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "Secret.md"), []byte("do not read"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Asking for a private folder is a caller bug, and failing loudly is the only way it
	// surfaces before the content reaches generated output.
	if _, err := Collect(settings, []string{settings.Private[0]}); err == nil {
		t.Fatal("Collect walked a private folder instead of refusing")
	}
}

func TestCollectReadsOnlySharedFolders(t *testing.T) {
	root := t.TempDir()
	settings := testConfig(root)
	shared := filepath.Join(root, settings.Shared[0])
	private := filepath.Join(root, settings.Private[0])
	for _, folder := range []string{shared, private} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(shared, "Visible.md"),
		[]byte("---\ntype: project\nstatus: active\n---\n# Visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "Secret.md"), []byte("do not read"), 0o644); err != nil {
		t.Fatal(err)
	}

	notes, err := Collect(settings, settings.IndexFolders())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Title != "Visible" {
		t.Fatalf("Collect() = %#v, want only Visible", notes)
	}
}

func TestSharedNoteCanNarrowScope(t *testing.T) {
	root := t.TempDir()
	settings := testConfig(root)
	folder := filepath.Join(root, settings.Shared[0])
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntype: project\nstatus: active\nscope: private\n---\n# Hidden\n"
	if err := os.WriteFile(filepath.Join(folder, "Hidden.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	notes, err := Collect(settings, settings.IndexFolders())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("Collect() returned a scope: private note: %#v", notes)
	}
}

func TestRememberRefusesSecrets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Note.md")
	if err := os.WriteFile(path, []byte("---\ntype: project\nstatus: active\n---\n# Note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	note := &Note{Abs: path, Path: "Note.md", Title: "Note"}

	if err := Remember(note, "we chose Postgres over SQLite for concurrent writes"); err != nil {
		t.Fatalf("Remember rejected an ordinary fact: %v", err)
	}
	if err := Remember(note, "deploy key AKIAIOSFODNN7EXAMPLE"); err == nil {
		t.Error("Remember accepted a line containing an AWS key")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), AgentMemoryHeading) {
		t.Error("Remember did not create the agent-memory section")
	}
	if strings.Contains(string(body), "AKIA") {
		t.Error("a secret was written to the note")
	}
}
