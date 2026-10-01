// Package search ranks notes against a query.
//
// Both the CLI and the MCP server use this, so a person and an agent get the same
// answers. Scoring is BM25 over stemmed tokens with per-field weights, plus a small
// boost passed along wikilinks; see score.go for why there are no embeddings.
package search

import (
	"sort"
	"strings"

	"github.com/joeykokinda/aimem/internal/vault"
)

// Query is a search request. Empty filters match everything, so a bare Terms search
// behaves like a ranked search and a bare filter search behaves like a listing.
type Query struct {
	Terms    []string
	Type     string
	Status   string
	Company  string
	Tag      string
	Limit    int
	Context  int        // body lines to return per hit
	Synonyms [][]string // groups whose members satisfy each other
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

// Run scores every note against the query and returns the best hits.
//
// Filters are applied before scoring, but the corpus statistics come from the whole
// vault: a term's rarity is a property of the vault, not of the filtered subset, so
// filtering must not change how a term is weighted.
func Run(notes []*vault.Note, query Query) []Hit {
	corpus := buildCorpus(notes)

	// Each query term becomes the set of tokens that satisfy it.
	var wanted [][]string
	for _, term := range query.Terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" {
			continue
		}
		wanted = append(wanted, expand(term, query.Synonyms))
	}

	scores := map[string]float64{}
	reasons := map[string][]string{}
	eligible := map[string]*document{}

	for _, document := range corpus.documents {
		if !matchesFilters(document.note, query) {
			continue
		}
		eligible[document.note.Path] = document

		if len(wanted) == 0 {
			scores[document.note.Path] = 1
			reasons[document.note.Path] = []string{"filter match"}
			continue
		}

		total := 0.0
		var matched []string
		satisfied := true
		for _, alternatives := range wanted {
			best := 0.0
			var bestField string
			for _, token := range alternatives {
				for field := range fieldWeights {
					if points := corpus.score(document, field, token); points > best {
						best = points
						bestField = field
					}
				}
			}
			// Conjunctive: every term must appear somewhere, or the note is not a hit.
			// Without this a two-word query returns everything mentioning either word.
			if best == 0 {
				satisfied = false
				break
			}
			total += best
			matched = append(matched, bestField)
		}
		if !satisfied {
			continue
		}
		scores[document.note.Path] = total
		reasons[document.note.Path] = matched
	}

	if len(wanted) > 0 {
		applyLinkBoost(notes, eligible, scores, reasons)
	}

	var hits []Hit
	for path, score := range scores {
		document := eligible[path]
		if document == nil {
			continue
		}
		note := document.note
		hits = append(hits, Hit{
			Note: note, Title: note.Title, Path: note.Path, Type: note.Type,
			Status: note.Status, Summary: note.Summary,
			// Scaled to an integer so output is stable and readable; relative order is
			// what carries meaning, not the absolute number.
			Score:  int(score*10 + 0.5),
			Reason: strings.Join(distinct(reasons[path]), "+"),
			Lines:  matchingLines(note, wanted, query.Context),
		})
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

// applyLinkBoost lets a scoring note lift the notes it links to and the notes that link
// to it. A decision recorded in one note is often explained in its neighbour, so the
// neighbour is worth surfacing even when it does not contain the query terms as strongly.
//
// Only already-matching notes are lifted: the boost reorders results, it never adds a
// note that failed the conjunctive test. Otherwise one strong hit would drag in its whole
// neighbourhood regardless of relevance.
func applyLinkBoost(notes []*vault.Note, eligible map[string]*document,
	scores map[string]float64, reasons map[string][]string) {

	pathByTitle := map[string]string{}
	for _, note := range notes {
		pathByTitle[note.Title] = note.Path
	}

	// Snapshot the direct scores so a boost cannot cascade through the graph.
	direct := make(map[string]float64, len(scores))
	for path, score := range scores {
		direct[path] = score
	}

	for _, note := range notes {
		source, scored := direct[note.Path]
		if !scored {
			continue
		}
		for _, link := range note.Links {
			target, known := pathByTitle[link]
			if !known || target == note.Path {
				continue
			}
			if _, isHit := scores[target]; !isHit {
				continue
			}
			if _, ok := eligible[target]; !ok {
				continue
			}
			scores[target] += source * linkBoost
			reasons[target] = append(reasons[target], "linked")
		}
	}
}

// matchingLines returns the body lines that contain any wanted token, for context.
func matchingLines(note *vault.Note, wanted [][]string, limit int) []Line {
	if limit <= 0 || len(wanted) == 0 {
		return nil
	}
	tokens := map[string]bool{}
	for _, alternatives := range wanted {
		for _, token := range alternatives {
			tokens[token] = true
		}
	}

	var lines []Line
	for index, line := range note.Lines {
		for _, candidate := range tokenize(line) {
			if tokens[candidate] {
				lines = append(lines, Line{Number: index + 1, Text: strings.TrimSpace(line)})
				break
			}
		}
		if len(lines) >= limit {
			break
		}
	}
	return lines
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

func distinct(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value != "" && !seen[value] {
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
