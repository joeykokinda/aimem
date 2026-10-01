// Package profile derives a picture of what the vault's owner works on and knows.
//
// # WHY THIS IS DERIVED AND NOT WRITTEN DOWN
//
// "What am I good at" and "what am I working on" are exactly the facts a hand-maintained
// note gets wrong. Someone writes "Languages: Go, Rust, Python" once, then spends a year
// in TypeScript and never updates it, and an agent reading that note is now worse off
// than one that read nothing.
//
// Everything here comes from evidence that updates itself: the languages are the files in
// the repositories the vault actually claims, the subjects are the tags on notes that have
// been touched recently, and the connections are the shared tags, companies and wikilinks
// already in the vault. Nothing is maintained, so nothing rots.
//
// What cannot be derived is access: which accounts exist, which services are paid for,
// what hardware is on hand. Those belong in an ordinary reference note, which is already
// indexed and searchable; this package does not try to guess them.
package profile

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/vault"
)

// Profile is the derived picture.
type Profile struct {
	Languages   []Weighted `json:"languages"`
	Subjects    []Weighted `json:"subjects"`
	Companies   []Weighted `json:"companies"`
	Clusters    []Cluster  `json:"clusters"`
	ActiveNow   []string   `json:"active_now"`
	Capability  []string   `json:"capability_notes"`
	ReposWalked int        `json:"repos_walked"`
}

// Weighted is one item and how much evidence there is for it.
type Weighted struct {
	Name     string   `json:"name"`
	Count    int      `json:"count"`
	Active   int      `json:"active"` // how much of the count comes from active work
	Examples []string `json:"examples,omitempty"`
}

// Cluster is a set of notes that belong together, and why.
type Cluster struct {
	Reason string   `json:"reason"`
	Notes  []string `json:"notes"`
}

// languageByExtension maps a file extension to a language name. Deliberately short: the
// point is to describe what someone works in, not to be a linguist. An extension nobody
// recognizes contributes nothing rather than a wrong label.
var languageByExtension = map[string]string{
	".go": "Go", ".rs": "Rust", ".c": "C", ".h": "C", ".cpp": "C++", ".hpp": "C++",
	".ts": "TypeScript", ".tsx": "TypeScript", ".js": "JavaScript", ".jsx": "JavaScript",
	".py": "Python", ".rb": "Ruby", ".java": "Java", ".kt": "Kotlin", ".swift": "Swift",
	".sol": "Solidity", ".sh": "Shell", ".fish": "Shell", ".lua": "Lua", ".zig": "Zig",
	".php": "PHP", ".cs": "C#", ".ex": "Elixir", ".exs": "Elixir", ".hs": "Haskell",
	".sql": "SQL", ".vim": "Vimscript", ".nix": "Nix",
}

// skipDirectories are not the author's code. Counting them would say more about the
// ecosystem's dependencies than about the person.
var skipDirectories = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".next": true, "__pycache__": true,
	".venv": true, "venv": true, ".cache": true, "out": true, "bin": true,
	"third_party": true, "deps": true, ".terraform": true,
}

// filesPerRepo caps the walk. A profile is a shape, not a census, and one generated
// directory should not be allowed to dominate it or make a refresh slow.
const filesPerRepo = 4000

// Build derives the profile from the vault and the repositories it claims.
func Build(settings *config.Config, notes []*vault.Note, lastTouched map[string]string) *Profile {
	built := &Profile{}

	languages := map[string]*Weighted{}
	subjects := map[string]*Weighted{}
	companies := map[string]*Weighted{}

	for _, note := range notes {
		if note.Unreviewed() {
			continue // unreviewed guesses must not shape the picture of what someone knows
		}
		live := isLive(note, settings, lastTouched)

		for _, tag := range note.Tags {
			tag = strings.ToLower(strings.TrimSpace(tag))
			// Vault scaffolding is not a subject the person works on.
			if tag == "" || tag == "meta" || tag == "generated" {
				continue
			}
			tally(subjects, tag, live, note.Title)
		}
		if note.Company != "" {
			tally(companies, note.Company, live, note.Title)
		}
		if live && (note.Type == "project" || note.Type == "company") {
			built.ActiveNow = append(built.ActiveNow, note.Title)
		}
		// A note tagged for access or hardware is where the undeducible facts live.
		for _, tag := range note.Tags {
			switch strings.ToLower(tag) {
			case "host", "access", "capability", "hardware", "account", "infra":
				built.Capability = append(built.Capability, note.Title)
			}
		}

		if note.Repo == "" || !note.RepoOK {
			continue
		}
		built.ReposWalked++
		for language, count := range languagesIn(note.Repo) {
			entry := tallyGet(languages, language)
			entry.Count += count
			if live {
				entry.Active += count
			}
			if len(entry.Examples) < 3 && !contains(entry.Examples, note.Title) {
				entry.Examples = append(entry.Examples, note.Title)
			}
		}
	}

	built.Languages = rank(languages)
	built.Subjects = rank(subjects)
	built.Companies = rank(companies)
	built.Clusters = clusters(notes)
	built.Capability = distinct(built.Capability)
	sort.Strings(built.ActiveNow)
	return built
}

// isLive reports whether a note represents work in progress, counting journal mentions as
// well as edits, so the profile reflects now rather than ever.
func isLive(note *vault.Note, settings *config.Config, lastTouched map[string]string) bool {
	if note.Status != "active" {
		return false
	}
	age := note.AgeDays
	if journal, ok := lastTouched[note.Title]; ok {
		if parsed, err := time.Parse("2006-01-02", journal); err == nil {
			if days := int(time.Since(parsed).Hours() / 24); days < age {
				age = days
			}
		}
	}
	return age <= settings.StaleDays
}

// languagesIn counts source files per language in a checkout.
func languagesIn(repo string) map[string]int {
	counts := map[string]int{}
	seen := 0
	filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirectories[info.Name()] || strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > filesPerRepo {
			return filepath.SkipAll
		}
		if language, known := languageByExtension[strings.ToLower(filepath.Ext(path))]; known {
			counts[language]++
		}
		return nil
	})
	return counts
}

// clusters groups notes that share a company, a tag, or a wikilink. This is the "connect
// them" half: an agent that knows two projects share a stack can carry a lesson from one
// to the other instead of relearning it.
func clusters(notes []*vault.Note) []Cluster {
	byTag := map[string][]string{}
	for _, note := range notes {
		if note.Unreviewed() || note.Type == "dashboard" {
			continue
		}
		for _, tag := range note.Tags {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" || tag == "meta" || tag == "generated" {
				continue
			}
			byTag[tag] = append(byTag[tag], note.Title)
		}
	}

	var found []Cluster
	for tag, members := range byTag {
		// Two notes sharing a tag is a coincidence; three is a theme.
		if len(members) < 3 {
			continue
		}
		sort.Strings(members)
		found = append(found, Cluster{Reason: tag, Notes: distinct(members)})
	}
	sort.Slice(found, func(a, b int) bool {
		if len(found[a].Notes) != len(found[b].Notes) {
			return len(found[a].Notes) > len(found[b].Notes)
		}
		return found[a].Reason < found[b].Reason
	})
	if len(found) > 12 {
		found = found[:12]
	}
	return found
}

func tally(into map[string]*Weighted, name string, live bool, example string) {
	entry := tallyGet(into, name)
	entry.Count++
	if live {
		entry.Active++
	}
	if len(entry.Examples) < 3 && !contains(entry.Examples, example) {
		entry.Examples = append(entry.Examples, example)
	}
}

func tallyGet(into map[string]*Weighted, name string) *Weighted {
	if entry, ok := into[name]; ok {
		return entry
	}
	entry := &Weighted{Name: name}
	into[name] = entry
	return entry
}

// rank orders by live evidence first, then by total. Something someone did a lot of two
// years ago should rank below something they are doing now.
func rank(items map[string]*Weighted) []Weighted {
	var out []Weighted
	for _, entry := range items {
		out = append(out, *entry)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Active != out[b].Active {
			return out[a].Active > out[b].Active
		}
		if out[a].Count != out[b].Count {
			return out[a].Count > out[b].Count
		}
		return out[a].Name < out[b].Name
	})
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
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
	sort.Strings(out)
	return out
}
