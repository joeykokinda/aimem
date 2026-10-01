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

func fixture() []*vault.Note {
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
	hits := Run(fixture(), Query{Terms: []string{"ordering"}, Limit: 10, Context: 2})
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
	hits := Run(fixture(), Query{Terms: []string{"hcs", "ordering"}, Limit: 10})
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (both notes contain both terms)", len(hits))
	}
	for _, hit := range hits {
		if hit.Title == "Payroll" {
			t.Error("a note missing one of the terms was returned")
		}
	}
	if found := Run(fixture(), Query{Terms: []string{"hcs", "nonexistentword"}, Limit: 10}); len(found) != 0 {
		t.Errorf("got %d hits for an unsatisfiable query, want 0", len(found))
	}
}

func TestFilters(t *testing.T) {
	notes := fixture()
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
	if got := Run(fixture(), Query{Type: "project", Limit: 1}); len(got) != 1 {
		t.Errorf("got %d hits, want 1", len(got))
	}
}

func TestResolve(t *testing.T) {
	notes := fixture()
	for _, reference := range []string{"Hedera", "hedera", "HEDERA", "project/Hedera.md", "project/Hedera"} {
		if got := Resolve(notes, reference); got == nil || got.Title != "Hedera" {
			t.Errorf("Resolve(%q) did not find Hedera", reference)
		}
	}
	if got := Resolve(notes, "Nothing"); got != nil {
		t.Errorf("Resolve found a note that does not exist: %v", got)
	}
}

// TestStemmingMatchesWordForms is the first thing substring matching got wrong: a note
// about "deploying" was invisible to a search for "deployment".
func TestStemmingMatchesWordForms(t *testing.T) {
	notes := []*vault.Note{
		note("Release", "project", "active", "", "How we ship.", nil,
			"We are deploying the worker every Friday."),
	}
	for _, term := range []string{"deploy", "deployed", "deploying", "deployment", "deploys"} {
		if hits := Run(notes, Query{Terms: []string{term}, Limit: 5}); len(hits) == 0 {
			t.Errorf("searching %q found nothing in a note about deploying", term)
		}
	}
	// And it must not collapse unrelated words.
	if hits := Run(notes, Query{Terms: []string{"deplorable"}, Limit: 5}); len(hits) != 0 {
		t.Error("stemming matched an unrelated word")
	}
}

func TestStem(t *testing.T) {
	same := [][]string{
		{"deploy", "deployed", "deploying", "deployment", "deploys"},
		{"repository", "repositories"},
		{"ship", "shipping", "shipped"},
		{"index", "indexes"},
	}
	for _, group := range same {
		first := stem(group[0])
		for _, word := range group[1:] {
			if got := stem(word); got != first {
				t.Errorf("stem(%q) = %q, want %q (same as %q)", word, got, first, group[0])
			}
		}
	}
	// Short words are left alone rather than mangled.
	for _, word := range []string{"go", "api", "db", "hcs"} {
		if got := stem(word); got != word {
			t.Errorf("stem(%q) = %q, want it unchanged", word, got)
		}
	}
}

// TestSynonymsAreConfigured covers the "auth vs login flow" case. The groups come from
// the vault config because the person keeping the notes is the one who knows which words
// mean the same thing in them.
func TestSynonymsAreConfigured(t *testing.T) {
	notes := []*vault.Note{
		note("Login", "project", "active", "", "The login flow.", nil,
			"Users sign in through the login flow."),
	}
	if hits := Run(notes, Query{Terms: []string{"auth"}, Limit: 5}); len(hits) != 0 {
		t.Error("auth matched login with no synonym group configured")
	}
	groups := [][]string{{"auth", "authentication", "login", "signin"}}
	hits := Run(notes, Query{Terms: []string{"auth"}, Limit: 5, Synonyms: groups})
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want the login note via its synonym group", len(hits))
	}
}

// TestFrequencySaturates is what BM25 buys over counting matches: a note that repeats a
// term must not outrank the note the term is actually about.
func TestFrequencySaturates(t *testing.T) {
	notes := []*vault.Note{
		note("Consensus", "research", "evergreen", "", "What consensus means.",
			[]string{"distributed"}, "A short note."),
		note("Chatter", "project", "active", "", "Unrelated work.", nil,
			strings.Repeat("consensus consensus consensus\n", 40)),
	}
	hits := Run(notes, Query{Terms: []string{"consensus"}, Limit: 5})
	if len(hits) < 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Title != "Consensus" {
		t.Errorf("top hit = %q, want Consensus (title match beats 120 body repeats)", hits[0].Title)
	}
}

// TestRareTermsCountForMore is the other half of BM25: a term in every note carries less
// information than one in a single note.
func TestRareTermsCountForMore(t *testing.T) {
	var notes []*vault.Note
	for _, title := range []string{"One", "Two", "Three", "Four"} {
		notes = append(notes, note(title, "project", "active", "", "", nil,
			"This note mentions kubernetes."))
	}
	notes = append(notes, note("Five", "project", "active", "", "", nil,
		"This note mentions kubernetes and also hedera."))

	hits := Run(notes, Query{Terms: []string{"hedera"}, Limit: 10})
	if len(hits) != 1 || hits[0].Title != "Five" {
		t.Fatalf("rare term did not isolate its note: %#v", hits)
	}
	common := Run(notes, Query{Terms: []string{"kubernetes"}, Limit: 10})
	if len(common) != 5 {
		t.Errorf("got %d hits for the common term, want all 5", len(common))
	}
	if hits[0].Score <= common[0].Score {
		t.Error("a term in one note scored no higher than a term in every note")
	}
}

// TestLinkBoostReordersButNeverAdds pins the limit on graph influence: being linked from
// a relevant note can move a hit up, but it can never turn a non-match into a hit.
func TestLinkBoostReordersButNeverAdds(t *testing.T) {
	strong := note("Hedera", "project", "active", "", "Consensus work.",
		[]string{"hedera"}, "We use HCS for ordering. See [[Sidecar]] and [[Unrelated]].")
	strong.Links = []string{"Sidecar", "Unrelated"}
	sidecar := note("Sidecar", "project", "active", "", "", nil, "A note that mentions hcs once.")
	unrelated := note("Unrelated", "project", "active", "", "", nil, "Nothing relevant here.")

	hits := Run([]*vault.Note{strong, sidecar, unrelated},
		Query{Terms: []string{"hcs"}, Limit: 10})

	for _, hit := range hits {
		if hit.Title == "Unrelated" {
			t.Error("the link boost added a note that matched no term")
		}
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want Hedera and Sidecar", len(hits))
	}
	var sidecarHit Hit
	for _, hit := range hits {
		if hit.Title == "Sidecar" {
			sidecarHit = hit
		}
	}
	if !strings.Contains(sidecarHit.Reason, "linked") {
		t.Errorf("Sidecar reason = %q, want it to mention the link boost", sidecarHit.Reason)
	}
}
