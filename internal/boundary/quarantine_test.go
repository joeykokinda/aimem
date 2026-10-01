package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/index"
	"github.com/joeykokinda/aimem/internal/testvault"
	"github.com/joeykokinda/aimem/internal/vault"
)

// TestAgentWritesStayOutOfTheIndex is the property that makes free agent capture safe.
// Junk on disk costs nothing; junk in the file every session loads costs on every turn.
// So an agent may write anything, and nothing it writes reaches the index until promoted.
func TestAgentWritesStayOutOfTheIndex(t *testing.T) {
	settings := testvault.Build(t)

	// A note an agent invented whole.
	testvault.Write(t, settings.Root, "Projects/Invented.md", `---
type: project
status: active
origin: agent
---
# Invented

An agent guessed this project exists. It should not reach the index.
`)
	// And a fact an agent appended to a real note.
	alpha := filepath.Join(settings.Root, "Projects", "Alpha.md")
	body, err := os.ReadFile(alpha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alpha, append(body,
		[]byte("\n## Agent memory\n- 2026-09-30 (unreviewed): a guess about Alpha nobody checked\n")...),
		0o644); err != nil {
		t.Fatal(err)
	}

	result, err := index.Build(settings, "", true)
	if err != nil {
		t.Fatal(err)
	}
	brain, err := os.ReadFile(result.BrainPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(brain)

	if strings.Contains(text, "An agent guessed") {
		t.Error("an unreviewed note's content reached the index")
	}
	if strings.Contains(text, "a guess about Alpha") {
		t.Error("an unreviewed fact reached the index")
	}
	// Named, not described: enough to know review is waiting, nothing more.
	if !strings.Contains(text, "Unreviewed (1)") || !strings.Contains(text, "Invented") {
		t.Error("the index does not report that something is waiting for review")
	}
	if result.PendingNotes != 1 || result.PendingFacts != 1 {
		t.Errorf("pending counts = %d notes, %d facts; want 1 and 1",
			result.PendingNotes, result.PendingFacts)
	}
	// The real note is still described normally.
	if !strings.Contains(text, "Alpha") {
		t.Error("quarantine suppressed a reviewed note")
	}
}

// TestPromoteAndDrop covers both triage outcomes.
func TestPromoteAndDrop(t *testing.T) {
	settings := testvault.Build(t)
	path := filepath.Join(settings.Root, "Projects", "Beta.md")

	notes, err := vault.Collect(settings, settings.IndexFolders())
	if err != nil {
		t.Fatal(err)
	}
	var beta *vault.Note
	for _, note := range notes {
		if note.Title == "Beta" {
			beta = note
		}
	}
	if beta == nil {
		t.Fatal("fixture note missing")
	}

	if err := vault.RememberAs(beta, "we picked SQLite over Postgres for one writer", true); err != nil {
		t.Fatal(err)
	}
	reread := func() *vault.Note {
		fresh, err := vault.Collect(settings, settings.IndexFolders())
		if err != nil {
			t.Fatal(err)
		}
		for _, note := range fresh {
			if note.Title == "Beta" {
				return note
			}
		}
		t.Fatal("note vanished")
		return nil
	}

	if facts := reread().UnreviewedFacts(); len(facts) != 1 {
		t.Fatalf("got %d unreviewed facts, want 1", len(facts))
	}

	promoted, err := vault.PromoteFacts(reread())
	if err != nil {
		t.Fatal(err)
	}
	if promoted != 1 {
		t.Errorf("promoted %d, want 1", promoted)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), vault.AgentMarker) {
		t.Error("the marker survived promotion")
	}
	if !strings.Contains(string(body), "SQLite over Postgres") {
		t.Error("promotion lost the fact")
	}
	if facts := reread().UnreviewedFacts(); len(facts) != 0 {
		t.Error("a promoted fact is still reported as unreviewed")
	}

	// Dropping removes the line entirely and leaves the rest of the note alone.
	if err := vault.RememberAs(reread(), "a second guess worth discarding", true); err != nil {
		t.Fatal(err)
	}
	dropped, err := vault.DropFacts(reread())
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Errorf("dropped %d, want 1", dropped)
	}
	body, _ = os.ReadFile(path)
	if strings.Contains(string(body), "second guess") {
		t.Error("drop left the fact behind")
	}
	if !strings.Contains(string(body), "SQLite over Postgres") {
		t.Error("drop removed a previously promoted fact")
	}
	if !strings.Contains(string(body), "Beta is paused work") {
		t.Error("drop damaged the note's own prose")
	}
}

// TestDuplicateFactsAreRefused covers the most common way an agent generates junk:
// re-deriving in a later session something it already wrote down.
func TestDuplicateFactsAreRefused(t *testing.T) {
	settings := testvault.Build(t)
	notes, err := vault.Collect(settings, settings.IndexFolders())
	if err != nil {
		t.Fatal(err)
	}
	var alpha *vault.Note
	for _, note := range notes {
		if note.Title == "Alpha" {
			alpha = note
		}
	}

	fact := "the sweep runs on a five minute cron"
	if err := vault.RememberAs(alpha, fact, true); err != nil {
		t.Fatal(err)
	}
	fresh, _ := vault.Collect(settings, settings.IndexFolders())
	for _, note := range fresh {
		if note.Title == "Alpha" {
			alpha = note
		}
	}
	if err := vault.RememberAs(alpha, fact, true); err == nil {
		t.Error("the same fact was written twice")
	}
}

// TestAgentCreatedNotesGoThroughMCP drives the real tool surface and checks the created
// note is marked, so the quarantine cannot be bypassed by the path agents actually use.
func TestAgentCreatedNotesGoThroughMCP(t *testing.T) {
	settings := testvault.Build(t)

	answer := exchangeRaw(t, settings, true,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vault_create_note",`+
			`"arguments":{"title":"Gamma","type":"project","status":"active","body":"## Goal\nSomething new."}}}`)
	if !strings.Contains(answer, "unreviewed") {
		t.Errorf("creation did not report the note as unreviewed: %s", answer)
	}

	created := filepath.Join(settings.Root, "Projects", "Gamma.md")
	body, err := os.ReadFile(created)
	if err != nil {
		t.Fatalf("note was not created where projects live: %v", err)
	}
	if !strings.Contains(string(body), "origin: agent") {
		t.Error("created note is not marked as agent-written")
	}

	// A second attempt at the same title must not clobber the first.
	again := exchangeRaw(t, settings, true,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vault_create_note",`+
			`"arguments":{"title":"Gamma","type":"project","status":"active","body":"different"}}}`)
	if !strings.Contains(again, "already exists") {
		t.Errorf("duplicate title was accepted: %s", again)
	}

	// Invalid type and status are refused rather than written.
	bad := exchangeRaw(t, settings, true,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vault_create_note",`+
			`"arguments":{"title":"Delta","type":"nonsense","status":"active","body":"x"}}}`)
	if !strings.Contains(bad, "is not one of") {
		t.Errorf("an invalid type was accepted: %s", bad)
	}

	// And a title that would escape the folder is refused.
	escape := exchangeRaw(t, settings, true,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vault_create_note",`+
			`"arguments":{"title":"../../escape","type":"project","status":"active","body":"x"}}}`)
	if !strings.Contains(escape, "not valid in a filename") {
		t.Errorf("a path-traversing title was accepted: %s", escape)
	}
}

// TestReadOnlyServerCannotCreateNotes keeps the default safe.
func TestReadOnlyServerCannotCreateNotes(t *testing.T) {
	settings := testvault.Build(t)
	listing := exchangeRaw(t, settings, false, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(listing, "vault_create_note") {
		t.Error("a read-only server advertises note creation")
	}
	refusal := exchangeRaw(t, settings, false,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"vault_create_note",`+
			`"arguments":{"title":"Nope","type":"project","status":"active","body":"x"}}}`)
	if !strings.Contains(refusal, "read-only") {
		t.Errorf("read-only server did not refuse creation: %s", refusal)
	}
	if _, err := os.Stat(filepath.Join(settings.Root, "Projects", "Nope.md")); err == nil {
		t.Error("a read-only server created a note")
	}
}
