package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/vault"
)

// TestOwnedBy is the replacement for a hardcoded list of absolute paths. That list was
// inert on any machine but the author's, which meant the guard against writing into
// other people's repositories silently stopped guarding.
func TestOwnedBy(t *testing.T) {
	tests := []struct {
		remote string
		owners []string
		want   bool
	}{
		{"git@github.com:joeykokinda/aimem.git", []string{"joeykokinda"}, true},
		{"https://github.com/joeykokinda/aimem.git", []string{"joeykokinda"}, true},
		{"https://github.com/joeykokinda/aimem", []string{"joeykokinda"}, true},
		{"ssh://git@github.com/joeykokinda/aimem.git", []string{"joeykokinda"}, true},
		{"git@github.com:JOEYKOKINDA/aimem.git", []string{"joeykokinda"}, true},
		{"git@github.com:someoneelse/thing.git", []string{"joeykokinda"}, false},
		{"git@gitlab.com:someoneelse/thing.git", []string{"joeykokinda", "otherorg"}, false},
		{"git@github.com:otherorg/thing.git", []string{"joeykokinda", "otherorg"}, true},
		{"not-a-url", []string{"joeykokinda"}, false},
	}
	for _, test := range tests {
		if got := ownedBy(test.remote, test.owners); got != test.want {
			t.Errorf("ownedBy(%q, %v) = %v, want %v", test.remote, test.owners, got, test.want)
		}
	}
}

// TestRemotelessRepoIsOurs pins the rule that decides ownership for local-only work.
// A checkout of someone else's project always carries the remote it came from, so a
// repository with no remote is local work and belongs to whoever is running aimem.
// The opposite default silently skipped every scratch repo on the machine.
func TestRemotelessRepoIsOurs(t *testing.T) {
	settings := config.Default(t.TempDir())
	settings.SyncOwners = []string{"someone"}
	if reason := skipReason(settings, t.TempDir()); reason != "" {
		t.Errorf("a local-only repository was skipped: %q", reason)
	}
	settings.SyncOwners = nil
	if reason := skipReason(settings, t.TempDir()); reason != "" {
		t.Errorf("with no allowlist every repo is in scope, got %q", reason)
	}
}

func TestSkipList(t *testing.T) {
	settings := config.Default(t.TempDir())
	settings.SyncSkip = []string{"/opt/vendor/*"}
	if reason := skipReason(settings, "/opt/vendor/thing"); reason == "" {
		t.Error("a glob in sync.skip did not match")
	}
	if reason := skipReason(settings, "/home/me/thing"); reason != "" {
		t.Errorf("an unrelated repo was skipped: %q", reason)
	}
}

// TestBlockIsIdempotentAndPreservesSurroundings is the property that makes this safe to
// run on every refresh: it must never accumulate copies, and must never touch a line
// someone wrote.
func TestBlockIsIdempotentAndPreservesSurroundings(t *testing.T) {
	repo := t.TempDir()
	settings := config.Default(t.TempDir())
	settings.Name = "Demo"
	note := &vault.Note{Title: "Alpha", Path: "Projects/Alpha.md", Status: "active", Repo: repo}

	claude := filepath.Join(repo, "CLAUDE.md")
	handwritten := "# My notes\n\nRun the tests with `go test ./...`.\n"
	if err := os.WriteFile(claude, []byte(handwritten), 0o644); err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 3; pass++ {
		if _, err := writeBlock(settings, note); err != nil {
			t.Fatal(err)
		}
	}

	body, err := os.ReadFile(claude)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if count := strings.Count(text, beginMarker); count != 1 {
		t.Errorf("found %d generated blocks after three passes, want 1", count)
	}
	if !strings.Contains(text, "Run the tests with") {
		t.Error("the generated block destroyed hand-written content")
	}
	if !strings.Contains(text, "Projects/Alpha.md") {
		t.Error("the block does not name the vault note")
	}
}

// TestStaleBlockIsReplaced keeps a rename from leaving two blocks behind. markerPairs is a
// list for exactly this case, and matching a begin marker against the wrong end marker is
// what previously caused a duplicate instead of a replacement.
func TestStaleBlockIsReplaced(t *testing.T) {
	repo := t.TempDir()
	settings := config.Default(t.TempDir())
	note := &vault.Note{Title: "Alpha", Path: "Projects/Alpha.md", Status: "active", Repo: repo}

	claude := filepath.Join(repo, "CLAUDE.md")
	stale := "# Repo\n\n" + beginMarker + "\nold generated text\n" + endMarker + "\n"
	if err := os.WriteFile(claude, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBlock(settings, note); err != nil {
		t.Fatal(err)
	}

	body, _ := os.ReadFile(claude)
	if strings.Contains(string(body), "old generated text") {
		t.Error("the stale block was not replaced")
	}
	if strings.Count(string(body), "vault context") != 1 {
		t.Error("replacing the legacy block did not produce exactly one new block")
	}
	if strings.Count(string(body), beginMarker) != 1 {
		t.Error("replacement left more than one block")
	}
}

// TestDeadAndUnmappedNotesAreIgnored keeps aimem from creating CLAUDE.md files in
// directories that no longer have a live note.
func TestDeadAndUnmappedNotesAreIgnored(t *testing.T) {
	repo := t.TempDir()
	settings := config.Default(t.TempDir())
	notes := []*vault.Note{
		{Title: "Dead", Path: "Projects/Dead.md", Status: "dead", Repo: repo},
		{Title: "NoRepo", Path: "Projects/NoRepo.md", Status: "active"},
		{Title: "Gone", Path: "Projects/Gone.md", Status: "active", Repo: "/nonexistent/path"},
	}
	outcome, err := Run(settings, notes, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Written) != 0 {
		t.Errorf("wrote %v, want nothing", outcome.Written)
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err == nil {
		t.Error("a dead note's repo got a CLAUDE.md")
	}
}

// TestEveryBlockIsRemoved covers a file that somehow ended up with two generated blocks.
// Replacing only the first leaves a stale one behind that contradicts the fresh one.
func TestEveryBlockIsRemoved(t *testing.T) {
	repo := t.TempDir()
	settings := config.Default(t.TempDir())
	note := &vault.Note{Title: "Alpha", Path: "Projects/Alpha.md", Status: "active", Repo: repo}

	claude := filepath.Join(repo, "CLAUDE.md")
	both := beginMarker + "\nstale text\n" + endMarker + "\n\n" +
		beginMarker + "\nnewer text\n" + endMarker + "\n\n# Hand-written\n\nKeep me.\n"
	if err := os.WriteFile(claude, []byte(both), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBlock(settings, note); err != nil {
		t.Fatal(err)
	}

	body, _ := os.ReadFile(claude)
	text := string(body)
	if strings.Contains(text, "stale text") || strings.Contains(text, "newer text") {
		t.Error("old generated content survived")
	}
	if strings.Count(text, beginMarker) != 1 {
		t.Errorf("found %d blocks, want exactly 1", strings.Count(text, beginMarker))
	}
	if !strings.Contains(text, "Keep me.") {
		t.Error("hand-written content was destroyed")
	}
	// The block belongs where it already was, not appended after the prose.
	if strings.Index(text, beginMarker) > strings.Index(text, "# Hand-written") {
		t.Error("the block moved to the end of the file instead of staying in place")
	}
}
