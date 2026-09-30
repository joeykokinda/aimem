// Package search ranks notes against a query.
//
// This replaces a raw ripgrep call. Substring grep over a vault has two problems: it
// returns matches with no sense of which note is actually about the subject, and it has
// no idea what frontmatter means, so it cannot answer "active projects tagged go". Both
// the CLI and the MCP server use this, so an agent and a human get the same answers.
//
// Ranking is deliberately simple and explainable: a title match beats a tag match beats a
// summary match beats a body match. A vault is hundreds of notes, not millions, so there
// is nothing here an index would speed up and nothing a model would rank better.
package search

import (
	"sort"
	"strings"

	"github.com/joeykokinda/aimem/internal/vault"
)

// Query is a search request. Empty filters match everything, so a bare Terms search
// behaves like a ranked grep and a bare filter search behaves like a listing.
type Query struct {
	Terms   []string
	Type    string
	Status  string
	Company string
	Tag     string
	Limit   int
	Context int // body lines to return per hit
}

// Line is one matching body line, with its 1-indexed position.
type Line struct {
	Number int    `json:"line"`
	Text   string `json:"text"`
}

// Hit is one matching note and why it matched.
type Hit struct {
	Note    *vault.Note `json:"-"`
	Title   string      `json:"title"`
	Path    string      `json:"path"`
	Type    string      `json:"type,omitempty"`
	Status  string      `json:"status,omitempty"`
	Summary string      `json:"summary,omitempty"`
	Score   int         `json:"score"`
	Reason  string      `json:"reason"`
	Lines   []Line      `json:"lines,omitempty"`
}

// Scoring weights. Kept as named constants because the ordering between them is the
// entire ranking model, and it should be readable without running anything.
const (
	scoreTitleExact  = 100
	scoreTitleWord   = 60
	scoreTitlePart   = 40
	scoreTag         = 30
	scoreCompany     = 25
	scoreSummary     = 15
	scoreBodyMatch   = 3
	scoreBodyMaximum = 30
)

// Run scores every note against the query and returns the best hits.
func Run(notes []*vault.Note, query Query) []Hit {
	terms := normalize(query.Terms)
	var hits []Hit

	for _, note := range notes {
		if !matchesFilters(note, query) {
			continue
		}
		// A filter-only query is a listing: everything passing the filters is a hit.
		if len(terms) == 0 {
			hits = append(hits, Hit{
				Note: note, Title: note.Title, Path: note.Path, Type: note.Type,
				Status: note.Status, Summary: note.Summary, Score: 1, Reason: "filter match",
			})
			continue
		}
		if hit, ok := score(note, terms, query.Context); ok {
			hits = append(hits, hit)
		}
	}

	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].Title < hits[b].Title
	})
	if query.Limit > 0 && len(hits) > query.Limit {
		hits = hits[:query.Limit]
	}
	return hits
}

// score requires every term to appear somewhere in the note, then ranks by where. The
// AND requirement is what stops a two-word query returning everything that mentions
// either word, which is the failure mode that made grep results unusable.
func score(note *vault.Note, terms []string, contextLines int) (Hit, bool) {
	title := strings.ToLower(note.Title)
	summary := strings.ToLower(note.Summary)
	company := strings.ToLower(note.Company)
	tags := strings.ToLower(strings.Join(note.Tags, " "))

	total := 0
	var reasons []string
	var lines []Line
	seenLine := map[int]bool{}

	for _, term := range terms {
		found := false

		switch {
		case title == term:
			total += scoreTitleExact
			reasons = append(reasons, "title")
			found = true
		case containsWord(title, term):
			total += scoreTitleWord
			reasons = append(reasons, "title")
			found = true
		case strings.Contains(title, term):
			total += scoreTitlePart
			reasons = append(reasons, "title")
			found = true
		}

		if strings.Contains(tags, term) {
			total += scoreTag
			reasons = append(reasons, "tag")
			found = true
		}
		if company != "" && strings.Contains(company, term) {
			total += scoreCompany
			reasons = append(reasons, "company")
			found = true
		}
		if strings.Contains(summary, term) {
			total += scoreSummary
			reasons = append(reasons, "summary")
			found = true
		}

		bodyPoints := 0
		for index, line := range note.Lines {
			if !strings.Contains(strings.ToLower(line), term) {
				continue
			}
			found = true
			if bodyPoints < scoreBodyMaximum {
				bodyPoints += scoreBodyMatch
			}
			if len(lines) < contextLines && !seenLine[index] {
				seenLine[index] = true
				lines = append(lines, Line{Number: index + 1, Text: strings.TrimSpace(line)})
			}
		}
		if bodyPoints > 0 {
			total += bodyPoints
			reasons = append(reasons, "body")
		}

		if !found {
			return Hit{}, false
		}
	}

	sort.Slice(lines, func(a, b int) bool { return lines[a].Number < lines[b].Number })
	return Hit{
		Note: note, Title: note.Title, Path: note.Path, Type: note.Type,
		Status: note.Status, Summary: note.Summary, Score: total,
		Reason: strings.Join(distinct(reasons), "+"), Lines: lines,
	}, true
}

func matchesFilters(note *vault.Note, query Query) bool {
	if query.Type != "" && !strings.EqualFold(note.Type, query.Type) {
		return false
	}
	if query.Status != "" && !strings.EqualFold(note.Status, query.Status) {
		return false
	}
	if query.Company != "" && !strings.EqualFold(note.Company, query.Company) {
		return false
	}
	if query.Tag != "" {
		matched := false
		for _, tag := range note.Tags {
			if strings.EqualFold(tag, query.Tag) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// containsWord reports a whole-word match, so "go" hits "go, web3" but not "google".
func containsWord(haystack, needle string) bool {
	for _, field := range strings.FieldsFunc(haystack, func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
	}) {
		if field == needle {
			return true
		}
	}
	return false
}

func normalize(terms []string) []string {
	var out []string
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" {
			out = append(out, term)
		}
	}
	return out
}

func distinct(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// Resolve finds one note by exact title, then case-insensitive title, then path. This is
// what `aimem show` and the MCP note tool use, so "read the Aimem note" works without
// anyone knowing where the file lives.
func Resolve(notes []*vault.Note, reference string) *vault.Note {
	reference = strings.TrimSpace(reference)
	trimmed := strings.TrimSuffix(reference, ".md")
	for _, note := range notes {
		if note.Title == trimmed {
			return note
		}
	}
	for _, note := range notes {
		if strings.EqualFold(note.Title, trimmed) {
			return note
		}
	}
	for _, note := range notes {
		if note.Path == reference || strings.TrimSuffix(note.Path, ".md") == trimmed {
			return note
		}
	}
	return nil
}
