// Package vault reads a Markdown vault: folder scoping, frontmatter parsing, and the
// small amount of structure both the index generator and the validator need.
//
// Scoping is driven entirely by config.Config. Nothing about any particular vault is
// compiled in here. Collect refuses to walk a folder the config places in the private or
// locked tier even if a caller asks it to, so a bug in a caller cannot turn into a leak.
package vault

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joeykokinda/aimem/internal/config"
)

type Note struct {
	Path     string   `json:"path"` // vault-relative
	Abs      string   `json:"-"`
	Title    string   `json:"title"`
	Type     string   `json:"type,omitempty"`
	Status   string   `json:"status,omitempty"`
	Company  string   `json:"company,omitempty"`
	Repo     string   `json:"repo,omitempty"`
	Horizon  string   `json:"horizon,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Links    []string `json:"links,omitempty"`
	RepoOK   bool     `json:"repo_exists,omitempty"`
	Modified string   `json:"modified,omitempty"` // filesystem mtime; unreliable, fallback only
	Updated  string   `json:"updated,omitempty"`  // last commit that touched this note
	AgeDays  int      `json:"age_days,omitempty"` // days since Updated

	Fields map[string]string `json:"-"`
	Body   string            `json:"-"`
	Lines  []string          `json:"-"`
}

// Collect walks the given vault-relative folders and returns their notes, sorted by
// path. Notes marked `scope: private` in frontmatter are skipped: scope can narrow what
// is exposed, never widen it.
//
// A folder the config places in the private or locked tier is refused outright rather
// than skipped quietly. Callers pass folder lists derived from the config, so being
// asked to walk a private folder means a caller is wrong, and failing loudly is the only
// way that surfaces before it reaches generated output.
func Collect(settings *config.Config, folders []string) ([]*Note, error) {
	root := settings.Root
	var notes []*Note
	for _, folder := range folders {
		if settings.IsPrivate(folder) {
			return nil, fmt.Errorf("refusing to walk %q: the vault config places it in the private tier", folder)
		}
		base := filepath.Join(root, folder)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			note, err := Parse(path, relative, info)
			if err != nil {
				return err
			}
			if note != nil {
				notes = append(notes, note)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(notes, func(a, b int) bool { return notes[a].Path < notes[b].Path })
	return notes, nil
}

func Parse(absolute, relative string, info os.FileInfo) (*Note, error) {
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return nil, err
	}
	body := string(raw)
	fields, rest := SplitFrontmatter(body)

	if strings.EqualFold(fields["scope"], "private") {
		return nil, nil
	}

	note := &Note{
		Path:     relative,
		Abs:      absolute,
		Title:    strings.TrimSuffix(filepath.Base(relative), ".md"),
		Type:     fields["type"],
		Status:   fields["status"],
		Company:  fields["company"],
		Repo:     fields["repo"],
		Horizon:  fields["horizon"],
		Tags:     SplitList(fields["tags"]),
		Summary:  FirstProse(rest),
		Links:    WikiLinks(rest),
		Modified: info.ModTime().Format("2006-01-02"),
		Fields:   fields,
		Body:     body,
		Lines:    strings.Split(body, "\n"),
	}
	if note.Repo != "" {
		if _, err := os.Stat(note.Repo); err == nil {
			note.RepoOK = true
		}
	}
	return note, nil
}

// SplitFrontmatter pulls flat `key: value` pairs out of a leading --- block and returns
// them with the remaining body. Nested YAML is not used anywhere in this vault, so a
// line scanner is enough.
func SplitFrontmatter(body string) (map[string]string, string) {
	fields := map[string]string{}
	if !strings.HasPrefix(body, "---\n") {
		return fields, body
	}
	end := strings.Index(body[4:], "\n---")
	if end < 0 {
		return fields, body
	}
	block := body[4 : 4+end]
	remainder := body[4+end:]
	if cut := strings.Index(remainder, "\n"); cut >= 0 {
		remainder = remainder[cut+1:]
	}
	remainder = strings.TrimPrefix(remainder, "--")
	remainder = strings.TrimPrefix(remainder, "-\n")

	for _, line := range strings.Split(block, "\n") {
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		value = strings.Trim(value, `"'`)
		if key != "" {
			fields[key] = value
		}
	}
	return fields, remainder
}

func HasFrontmatter(body string) bool { return strings.HasPrefix(body, "---\n") }

func SplitList(value string) []string {
	value = strings.Trim(value, "[]")
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var items []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		item = strings.Trim(item, `"'`)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

// FirstProse returns the first meaningful sentence of a note: the line under a
// "## Goal" heading if there is one, otherwise the first non-heading paragraph.
func FirstProse(body string) string {
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## Goal") {
			for _, candidate := range lines[index+1:] {
				candidate = strings.TrimSpace(candidate)
				if candidate == "" {
					continue
				}
				if strings.HasPrefix(candidate, "#") {
					break
				}
				return clip(candidate)
			}
			break
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "```") ||
			strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		return clip(trimmed)
	}
	return ""
}

func clip(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

// WikiLinks returns the distinct [[targets]] in a body, with any |alias stripped.
// Code is stripped first, so Dataview expressions like `= "[[" + date + "]]"` are not
// mistaken for links.
func WikiLinks(body string) []string {
	body = stripCode(body)
	seen := map[string]bool{}
	var links []string
	for {
		open := strings.Index(body, "[[")
		if open < 0 {
			break
		}
		body = body[open+2:]
		closeIndex := strings.Index(body, "]]")
		if closeIndex < 0 {
			break
		}
		target := body[:closeIndex]
		body = body[closeIndex+2:]
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = target[:pipe]
		}
		if hash := strings.Index(target, "#"); hash >= 0 {
			target = target[:hash]
		}
		target = strings.TrimSpace(target)
		if target != "" && !seen[target] {
			seen[target] = true
			links = append(links, target)
		}
	}
	return links
}

// stripCode removes fenced blocks and inline spans so their contents are not parsed as
// vault syntax.
func stripCode(body string) string {
	var out strings.Builder
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for {
			open := strings.Index(line, "`")
			if open < 0 {
				break
			}
			closeIndex := strings.Index(line[open+1:], "`")
			if closeIndex < 0 {
				line = line[:open]
				break
			}
			line = line[:open] + line[open+1+closeIndex+1:]
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

func Contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// GitDates maps vault-relative paths to the date of the most recent commit that touched
// them. This is the authoritative "last updated" signal: frontmatter `updated:` fields
// rot because they depend on someone remembering, and filesystem mtime is destroyed by
// a clone, a sync, or a backup script. Git already tracks it correctly for free.
//
// One `git log` pass covers the whole vault. Log order is newest-first, so the first
// date seen for a path is the one that counts. A vault that is not a git repository
// returns an empty map and callers fall back to mtime.
func GitDates(root string) map[string]string {
	command := exec.Command("git", "-c", "core.quotepath=false", "log",
		"--pretty=format:%x00%cs", "--name-only", "--no-renames")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return map[string]string{}
	}

	dates := map[string]string{}
	for _, record := range strings.Split(string(output), "\x00") {
		lines := strings.Split(strings.TrimSpace(record), "\n")
		if len(lines) < 2 {
			continue
		}
		date := strings.TrimSpace(lines[0])
		if len(date) != len("2006-01-02") {
			continue
		}
		for _, path := range lines[1:] {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if _, seen := dates[path]; !seen {
				dates[path] = date
			}
		}
	}
	return dates
}

// ApplyGitDates stamps each note's Updated field and the age derived from it. Notes with
// no commit yet (created but never committed) fall back to their filesystem mtime.
func ApplyGitDates(root string, notes []*Note) {
	dates := GitDates(root)
	now := time.Now()
	for _, note := range notes {
		updated := dates[note.Path]
		if updated == "" {
			updated = note.Modified
		}
		note.Updated = updated
		if parsed, err := time.Parse("2006-01-02", updated); err == nil {
			note.AgeDays = int(now.Sub(parsed).Hours() / 24)
		}
	}
}

// IsStale reports whether a note claims to be active but has not been touched since the
// configured threshold. The threshold is what makes `status:` falsifiable: without it
// every note stays active forever and the field carries no information.
//
// Only the configured staleable types are considered. A vault typically excludes ideas
// and research, which accrete rather than rot; warning about those would train you to
// ignore the warning that matters.
func (n *Note) IsStale(settings *config.Config) bool {
	return n.Status == "active" &&
		n.AgeDays > settings.StaleDays &&
		Contains(settings.StaleableTypes, n.Type)
}

// AgentMemoryHeading is the section durable agent-written facts are appended under, kept
// separate from hand-written prose so a human can see at a glance what a tool wrote.
const AgentMemoryHeading = "## Agent memory"

// Remember appends a dated fact to a note's agent-memory section.
//
// The fact is scanned for secrets first. This is the one path where text an agent
// composed enters a vault that gets committed and pushed, so refusing here is cheaper
// than catching it in the validator after it is already in the history.
func Remember(note *Note, fact string) error {
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return fmt.Errorf("nothing to remember")
	}
	if name := MatchSecret(fact); name != "" {
		return fmt.Errorf("refusing to write: this looks like a %s, and the vault is pushed to a remote", name)
	}

	existing, err := os.ReadFile(note.Abs)
	if err != nil {
		return err
	}
	body := strings.TrimRight(string(existing), "\n")
	if !strings.Contains(body, AgentMemoryHeading) {
		body += "\n\n" + AgentMemoryHeading
	}
	body += fmt.Sprintf("\n- %s: %s\n", time.Now().Format("2006-01-02"), fact)
	return os.WriteFile(note.Abs, []byte(body), 0o644)
}
