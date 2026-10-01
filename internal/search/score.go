// BM25 scoring, stemming, and synonym expansion.
//
// The previous version matched substrings. That fails in both directions: "deployment"
// did not match a note about "deploying", and a note mentioning a word forty times
// outranked the note the word is actually about. BM25 fixes the second (term frequency
// saturates, and rare terms count for more than common ones) and a stemmer fixes the
// first.
//
// There are no embeddings here on purpose. A vault is hundreds of notes, and a model
// would mean a background service, a download, and results that change between runs.
// Everything below is deterministic: the same query always returns the same ranking.
package search

import (
	"math"
	"strings"

	"github.com/joeykokinda/aimem/internal/vault"
)

// BM25 parameters. k1 controls how fast term frequency saturates; b controls how much a
// long note is penalized for its length.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Field weights. A term in the title says the note is *about* the subject; the same term
// in the body says only that it came up.
var fieldWeights = map[string]float64{
	"title":   8.0,
	"tags":    5.0,
	"company": 3.0,
	"summary": 2.0,
	"body":    1.0,
}

// linkBoost is how much a note inherits from its neighbours in the wikilink graph. Small
// on purpose: being linked from a relevant note is weak evidence, not a reason to outrank
// a direct match.
const linkBoost = 0.15

// document is one note reduced to the per-field token counts BM25 needs.
type document struct {
	note   *vault.Note
	fields map[string]map[string]int
	length map[string]int
}

// corpus holds the tokenized notes and the statistics BM25 needs across all of them.
type corpus struct {
	documents  []*document
	docCount   map[string]map[string]int // field -> term -> number of documents containing it
	meanLength map[string]float64
}

func buildCorpus(notes []*vault.Note) *corpus {
	built := &corpus{
		docCount:   map[string]map[string]int{},
		meanLength: map[string]float64{},
	}
	totals := map[string]int{}

	for _, note := range notes {
		document := &document{
			note:   note,
			fields: map[string]map[string]int{},
			length: map[string]int{},
		}
		raw := map[string]string{
			"title":   note.Title,
			"tags":    strings.Join(note.Tags, " "),
			"company": note.Company,
			"summary": note.Summary,
			"body":    note.Body,
		}
		for field, text := range raw {
			counts := map[string]int{}
			tokens := tokenize(text)
			for _, token := range tokens {
				counts[token]++
			}
			document.fields[field] = counts
			document.length[field] = len(tokens)
			totals[field] += len(tokens)

			if built.docCount[field] == nil {
				built.docCount[field] = map[string]int{}
			}
			for token := range counts {
				built.docCount[field][token]++
			}
		}
		built.documents = append(built.documents, document)
	}

	for field, total := range totals {
		if len(built.documents) > 0 {
			built.meanLength[field] = float64(total) / float64(len(built.documents))
		}
	}
	return built
}

// score returns the BM25 contribution of one term in one field.
func (c *corpus) score(document *document, field, term string) float64 {
	frequency := float64(document.fields[field][term])
	if frequency == 0 {
		return 0
	}
	documentCount := float64(len(c.documents))
	containing := float64(c.docCount[field][term])
	// Standard BM25 idf, plus one so a term in every document still scores above zero
	// rather than going negative.
	idf := math.Log(1 + (documentCount-containing+0.5)/(containing+0.5))

	length := float64(document.length[field])
	mean := c.meanLength[field]
	if mean == 0 {
		mean = 1
	}
	norm := frequency * (bm25K1 + 1) /
		(frequency + bm25K1*(1-bm25B+bm25B*length/mean))
	return idf * norm * fieldWeights[field]
}

// tokenize lowercases, splits on anything that is not a letter or digit, and stems.
func tokenize(text string) []string {
	var tokens []string
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(field) < 2 && field != "c" && field != "r" && field != "go" {
			continue
		}
		if stopWords[field] {
			continue
		}
		tokens = append(tokens, stem(field))
	}
	return tokens
}

// stopWords are too common to carry meaning. Kept short: an aggressive list would drop
// terms that matter in a technical vault, and BM25's idf already discounts common words.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "are": true, "was": true, "but": true, "not": true, "you": true,
	"all": true, "can": true, "has": true, "have": true, "its": true, "our": true,
	"out": true, "use": true, "via": true, "per": true, "into": true, "then": true,
}

// stem strips common English suffixes so "deploying", "deployed" and "deployment" all
// reduce to the same token.
//
// This is a light suffix stripper, not Porter. A full stemmer is a few hundred lines to
// handle cases a technical vault rarely contains, and over-stemming collides words that
// should stay distinct. Order matters: longest suffixes first.
func stem(word string) string {
	if len(word) <= 3 {
		return word
	}
	suffixes := []struct {
		suffix string
		min    int
	}{
		{"ization", 6}, {"iveness", 6}, {"fulness", 6}, {"ousness", 6},
		{"ational", 6}, {"tional", 5}, {"alize", 5}, {"ement", 5},
		{"ments", 5}, {"ition", 5}, {"ation", 5}, {"ingly", 5},
		{"ment", 4}, {"ness", 4}, {"able", 4}, {"ible", 4}, {"ance", 4},
		{"ence", 4}, {"tion", 4}, {"sion", 4}, {"ties", 4}, {"ies", 3},
		{"ing", 4}, {"ers", 4}, {"est", 4}, {"ive", 4}, {"ful", 4},
		{"ity", 4}, {"ous", 4}, {"ate", 4}, {"ed", 3}, {"ly", 3},
		{"er", 4}, {"es", 3}, {"s", 3},
	}
	for _, candidate := range suffixes {
		if len(word) > candidate.min && strings.HasSuffix(word, candidate.suffix) {
			trimmed := strings.TrimSuffix(word, candidate.suffix)
			// "ies" -> "y" keeps "repositories" and "repository" together.
			if candidate.suffix == "ies" || candidate.suffix == "ties" {
				return trimmed + "y"
			}
			// Undo a doubled consonant left by -ing/-ed: "shipping" -> "ship".
			if len(trimmed) > 2 && trimmed[len(trimmed)-1] == trimmed[len(trimmed)-2] &&
				!isVowel(trimmed[len(trimmed)-1]) {
				trimmed = trimmed[:len(trimmed)-1]
			}
			return trimmed
		}
	}
	return word
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// expand turns a query term into the set of tokens that should satisfy it: the term
// itself plus anything the vault's configured synonym groups put in the same group.
//
// Synonyms are configured rather than built in. A general-purpose list would guess wrong
// about a technical vault ("node" means different things to different people), and the
// person who knows that "auth" and "login" are the same thing here is the one keeping the
// notes.
func expand(term string, groups [][]string) []string {
	stemmed := stem(term)
	expanded := []string{stemmed}
	seen := map[string]bool{stemmed: true}
	for _, group := range groups {
		inGroup := false
		for _, member := range group {
			if stem(strings.ToLower(strings.TrimSpace(member))) == stemmed {
				inGroup = true
				break
			}
		}
		if !inGroup {
			continue
		}
		for _, member := range group {
			token := stem(strings.ToLower(strings.TrimSpace(member)))
			if token != "" && !seen[token] {
				seen[token] = true
				expanded = append(expanded, token)
			}
		}
	}
	return expanded
}
