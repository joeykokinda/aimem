package search

import (
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/vault"
)

func note(title, kind, status, company, summary string, tags []string, body string) *vault.Note {
	return &vault.Note{
		Title: title, Path: kind + "/" + title + ".md", Type: kind, Status: status,
		Company: company, Summary: summary, Tags: tags,
		Body: body, Lines: strings.Split(body, "\n"),
	}
}

func corpus() []*vault.Note {
	return []*vault.Note{
		note("Hedera", "project", "active", "Acme", "Consensus service work.",
			[]string{"go", "web3"}, "We chose HCS topics over a database for ordering."),
		note("Ordering", "research", "evergreen", "", "Notes on message ordering.",
			[]string{"distributed"}, "Ordering guarantees differ between Kafka and HCS."),
		note("Payroll", "project", "paused", "Acme", "Internal payroll tool.",
			[]string{"typescript"}, "Nothing about consensus here."),
	}
}

// TestTitleBeatsBody is the whole ranking model in one assertion: the note that is
// *about* a subject outranks the note that merely mentions it.
func TestTitleBeatsBody(t *testing.T) {
	hits := Run(corpus(), Query{Terms: []string{"ordering"}, Limit: 10, Context: 2})
	if len(hits) < 2 {
		t.Fatalf("got %d hits, want at least 2", len(hits))
	}
	if hits[0].Title != "Ordering" {
		t.Errorf("top hit = %q, want Ordering (title match outranks body match)", hits[0].Title)
	}
}

// TestTermsAreConjunctive is why this replaced grep: a two-word query must not return
// everything mentioning either word.
func TestTermsAreConjunctive(t *testing.T) {
	hits := Run(corpus(), Query{Terms: []string{"hcs", "ordering"}, Limit: 10})
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (both notes contain both terms)", len(hits))
	}
	for _, hit := range hits {
		if hit.Title == "Payroll" {
			t.Error("a note missing one of the terms was returned")
		}
	}
	if found := Run(corpus(), Query{Terms: []string{"hcs", "nonexistentword"}, Limit: 10}); len(found) != 0 {
		t.Errorf("got %d hits for an unsatisfiable query, want 0", len(found))
	}
}

func TestFilters(t *testing.T) {
	notes := corpus()
	tests := []struct {
		name  string
		query Query
		want  int
	}{
		{"by type", Query{Type: "project", Limit: 10}, 2},
		{"by status", Query{Status: "active", Limit: 10}, 1},
		{"by company", Query{Company: "Acme", Limit: 10}, 2},
		{"by tag", Query{Tag: "web3", Limit: 10}, 1},
		{"filters compose with terms", Query{Terms: []string{"consensus"}, Type: "project", Status: "active", Limit: 10}, 1},
		{"filter excludes non-matching term hits", Query{Terms: []string{"consensus"}, Status: "paused", Limit: 10}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Run(notes, test.query); len(got) != test.want {
				t.Errorf("got %d hits, want %d", len(got), test.want)
			}
		})
	}
}

func TestWholeWordTitleMatch(t *testing.T) {
	notes := []*vault.Note{
		note("Go", "project", "active", "", "", nil, "irrelevant body"),
		note("Google", "project", "active", "", "", nil, "irrelevant body"),
	}
	hits := Run(notes, Query{Terms: []string{"go"}, Limit: 10})
	if len(hits) == 0 || hits[0].Title != "Go" {
		t.Fatalf("expected the exact title to rank first, got %#v", hits)
	}
	if len(hits) > 1 && hits[1].Score >= hits[0].Score {
		t.Error("a substring match scored as high as an exact title match")
	}
}

func TestLimit(t *testing.T) {
	if got := Run(corpus(), Query{Type: "project", Limit: 1}); len(got) != 1 {
		t.Errorf("got %d hits, want 1", len(got))
	}
}

func TestResolve(t *testing.T) {
	notes := corpus()
	for _, reference := range []string{"Hedera", "hedera", "HEDERA", "project/Hedera.md", "project/Hedera"} {
		if got := Resolve(notes, reference); got == nil || got.Title != "Hedera" {
			t.Errorf("Resolve(%q) did not find Hedera", reference)
		}
	}
	if got := Resolve(notes, "Nothing"); got != nil {
		t.Errorf("Resolve found a note that does not exist: %v", got)
	}
}
