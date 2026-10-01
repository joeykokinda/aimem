package journal

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/vault"
)

func notes(titles ...string) []*vault.Note {
	var out []*vault.Note
	for _, title := range titles {
		out = append(out, &vault.Note{Title: title})
	}
	return out
}

func TestLinksProjectNames(t *testing.T) {
	corpus := notes("Starling", "Northwind", "Northwind Atlas", "Go")
	tests := []struct {
		name   string
		text   string
		want   string
		linked []string
	}{
		{"exact name", "deposit sweep works on Starling now",
			"deposit sweep works on [[Starling]] now", []string{"Starling"}},
		{"different casing keeps what was typed", "fixed starling sweep",
			"fixed [[Starling|starling]] sweep", []string{"Starling"}},
		{"longest title wins", "shipped Northwind Atlas today",
			"shipped [[Northwind Atlas]] today", []string{"Northwind Atlas"}},
		{"two projects", "moved Starling billing into Northwind",
			"moved [[Starling]] billing into [[Northwind]]", []string{"Northwind", "Starling"}},
		{"already linked stays put", "[[Starling]] sweep works",
			"[[Starling]] sweep works", []string{"Starling"}},
		{"no match", "read a paper about consensus", "read a paper about consensus", nil},
		{"word boundary", "going to the store", "going to the store", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			linked, got := link(test.text, corpus, "")
			if got != test.want {
				t.Errorf("text = %q, want %q", got, test.want)
			}
			if strings.Join(linked, ",") != strings.Join(test.linked, ",") {
				t.Errorf("linked = %v, want %v", linked, test.linked)
			}
		})
	}
}

// TestExistingLinkIsNotCorrupted is why linking skips text already inside brackets:
// linking "Northwind" into "[[Northwind Atlas]]" would produce nested, broken syntax.
func TestExistingLinkIsNotCorrupted(t *testing.T) {
	linked, got := link("shipped [[Northwind Atlas]] and fixed Starling",
		notes("Northwind", "Northwind Atlas", "Starling"), "")
	if strings.Contains(got, "[[Northwind Atlas|") || strings.Count(got, "[[") != 2 {
		t.Errorf("corrupted an existing link: %q", got)
	}
	if len(linked) != 2 {
		t.Errorf("linked = %v, want both notes", linked)
	}
}

func TestForcedProject(t *testing.T) {
	linked, got := link("the sweep finally works", notes("Starling"), "Starling")
	if !strings.HasPrefix(got, "[[Starling]]") {
		t.Errorf("forced link missing: %q", got)
	}
	if len(linked) != 1 {
		t.Errorf("linked = %v", linked)
	}
}

func TestAppendWritesAndReports(t *testing.T) {
	settings := config.Default(t.TempDir())
	corpus := notes("Starling")
	when := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	entry, err := Append(settings, corpus, "deposit sweep works on Starling", "", when)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Created || !entry.Reaches() {
		t.Errorf("entry = %+v, want a created note that reaches the timeline", entry)
	}
	if !strings.HasSuffix(entry.Path, "2026-09-30.md") {
		t.Errorf("path = %q, want today's daily note", entry.Path)
	}

	second, err := Append(settings, corpus, "nothing in particular", "", when)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Error("second call recreated the note instead of appending")
	}
	if second.Reaches() {
		t.Error("an unlinked line claimed it reaches the timeline")
	}

	body, err := os.ReadFile(entry.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "- ") != 2 {
		t.Errorf("expected two logged lines, got:\n%s", body)
	}
	if !strings.Contains(string(body), "type: journal") {
		t.Error("created note is missing frontmatter")
	}
}

func TestAppendRefusesSecretsAndBadJournals(t *testing.T) {
	settings := config.Default(t.TempDir())
	when := time.Now()

	if _, err := Append(settings, nil, "token ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", when); err == nil {
		t.Error("a secret was written to the journal")
	}

	// The journal must be private. Writing user prose into a shared folder would put it
	// straight into the index.
	shared := config.Default(t.TempDir())
	shared.Journal = shared.Shared[0]
	if _, err := Append(shared, nil, "anything", "", when); err == nil {
		t.Error("wrote to a journal outside the private tier")
	}

	disabled := config.Default(t.TempDir())
	disabled.Journal = ""
	if _, err := Append(disabled, nil, "anything", "", when); err == nil {
		t.Error("wrote with no journal configured")
	}
}

func TestAppendCommitsDedupesBySHA(t *testing.T) {
	settings := config.Default(t.TempDir())
	note := &vault.Note{Title: "Aimem", Repo: "/tmp/aimem"}
	when := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	commits := []Commit{
		{SHA: "aaa1111", Subject: "Fix the parser", Note: note},
		{SHA: "bbb2222", Subject: "Add a test", Note: note},
	}

	added, path, err := AppendCommits(settings, commits, when)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	// Running again must add nothing: this is what makes it safe on a timer or a hook.
	again, _, err := AppendCommits(settings, commits, when)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("re-run added %d lines, want 0", again)
	}

	// A genuinely new commit still lands.
	third := append(commits, Commit{SHA: "ccc3333", Subject: "Ship it", Note: note})
	added, _, err = AppendCommits(settings, third, when)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want only the new commit", added)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "aaa1111") != 1 {
		t.Error("a commit was recorded twice")
	}
	if !strings.Contains(text, "- [[Aimem]] Fix the parser (aaa1111)") {
		t.Errorf("line is not linked correctly:\n%s", text)
	}
	if strings.Count(text, "type: journal") != 1 {
		t.Error("frontmatter was duplicated")
	}
}

func TestAppendCommitsSkipsSecretsAndBadJournals(t *testing.T) {
	settings := config.Default(t.TempDir())
	note := &vault.Note{Title: "Aimem"}
	when := time.Now()

	added, _, err := AppendCommits(settings, []Commit{
		{SHA: "ddd4444", Subject: "set token ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Note: note},
		{SHA: "eee5555", Subject: "an ordinary commit", Note: note},
	}, when)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (the secret-bearing subject must be dropped)", added)
	}

	shared := config.Default(t.TempDir())
	shared.Journal = shared.Shared[0]
	if _, _, err := AppendCommits(shared, []Commit{{SHA: "f", Subject: "x", Note: note}}, when); err == nil {
		t.Error("wrote commits to a journal outside the private tier")
	}
}
