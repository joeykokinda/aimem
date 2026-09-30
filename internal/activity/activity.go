// Package activity derives a per-project timeline from the daily journal.
//
// # WHY THIS PACKAGE IS ALLOWED TO READ A PRIVATE FOLDER
//
// The journal folder is private: agents cannot open it, and that is deliberate. But the
// daily note is where project progress actually gets written, so sealing it also seals
// off the one signal that makes the vault feel current. This package is the narrow,
// audited bridge.
//
// The contract, which is the entire reason this is safe:
//
//   - It reads the single folder named by folders.journal and nothing else. Not the
//     inbox, not any other private folder, and never the locked folder.
//   - That folder must be in the private tier. The bridge narrows private access, it
//     never creates it, so a journal pointed anywhere else is refused.
//   - A line is emitted only if it links to a note that is already shared. A line with no
//     wikilink, or one linking only to private notes, never leaves the folder.
//   - A line tagged #private is dropped. %%Obsidian comments%% are stripped.
//   - Every emitted line is scanned for secrets and dropped if it matches.
//   - Nothing is printed to stdout but counts. The caller is often an AI agent, and
//     putting extracted prose on stdout would defeat the boundary it is protecting.
//
// EMITTED LINES BECOME AGENT-READABLE. Activity.md lands in 99-Meta, which is shared, so
// anything extracted here is readable by any agent with vault access. #private and %% %%
// are how a line stays out. That tradeoff was chosen on purpose: the timeline is only
// useful if agents can see it.
package activity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/vault"
)

var (
	datePattern    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	commentPattern = regexp.MustCompile(`%%[^%]*%%`)
)

// Entry is one extracted journal line, attributed to one project.
type Entry struct {
	Target string // the shared note the line links to
	Date   string // YYYY-MM-DD
	Text   string
}

// Result reports what extraction produced. Counts only: no prose, so this is safe to
// print in front of an agent.
type Result struct {
	Entries      []Entry
	FilesRead    int
	LinesSkipped int // had a link, but was excluded
	SecretsFound int

	// Unlinked counts journal lines that name a shared note in plain text but do not
	// link to it, so they never reach the timeline. Reporting this is the only way the
	// bridge's silence is distinguishable from having nothing to say: a vault where
	// nobody types brackets produces an empty timeline and no complaint.
	//
	// Only the note titles are recorded, never the prose. Titles are already shared.
	Unlinked map[string]int
}

// UnlinkedTitles returns the shared notes most often mentioned without a link.
func (r *Result) UnlinkedTitles(limit int) []string {
	type pair struct {
		title string
		count int
	}
	var pairs []pair
	for title, count := range r.Unlinked {
		pairs = append(pairs, pair{title, count})
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].count != pairs[b].count {
			return pairs[a].count > pairs[b].count
		}
		return pairs[a].title < pairs[b].title
	})
	var out []string
	for index, item := range pairs {
		if limit > 0 && index >= limit {
			break
		}
		out = append(out, fmt.Sprintf("%s (%d)", item.title, item.count))
	}
	return out
}

// Extract walks the configured journal folder and returns entries linking to any of
// sharedTitles. A vault with no journal configured produces an empty result.
func Extract(settings *config.Config, sharedTitles map[string]bool) (*Result, error) {
	result := &Result{Unlinked: map[string]int{}}
	if settings.Journal == "" {
		return result, nil
	}
	// Enforced here as well as in config.Validate, because this is the function that
	// actually opens the files. A bridge that can be pointed at an arbitrary folder is
	// not a bridge, it is a hole.
	if !settings.IsPrivate(settings.Journal) {
		return nil, fmt.Errorf("journal folder %q is not in the private tier; refusing to extract",
			settings.Journal)
	}
	base := filepath.Join(settings.Root, settings.Journal)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return result, nil
	}

	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result.FilesRead++

		fields, body := vault.SplitFrontmatter(string(raw))
		date := noteDate(path, fields, info)

		for _, line := range strings.Split(body, "\n") {
			entry, skipped, secret := lineEntry(line, date, sharedTitles)
			if entry == nil && !secret {
				for _, title := range mentionedTitles(line, sharedTitles) {
					result.Unlinked[title]++
				}
			}
			if secret {
				result.SecretsFound++
				continue
			}
			if skipped {
				result.LinesSkipped++
				continue
			}
			if entry != nil {
				result.Entries = append(result.Entries, *entry)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(result.Entries, func(a, b int) bool {
		return result.Entries[a].Date > result.Entries[b].Date
	})
	return result, nil
}

// lineEntry decides the fate of a single journal line. It returns an entry to emit, or
// reports that the line was deliberately skipped, or that it tripped the secret scanner.
func lineEntry(line, date string, sharedTitles map[string]bool) (entry *Entry, skipped, secret bool) {
	text := commentPattern.ReplaceAllString(line, "")
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "- ")
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, false
	}

	links := vault.WikiLinks(text)
	if len(links) == 0 {
		return nil, false, false
	}

	// Find the first link that names a note already visible to agents. A line that only
	// references private notes stays private.
	target := ""
	for _, link := range links {
		if sharedTitles[link] {
			target = link
			break
		}
	}
	if target == "" {
		return nil, true, false
	}

	if hasPrivateTag(text) {
		return nil, true, false
	}
	if vault.MatchSecret(text) != "" {
		return nil, false, true
	}

	return &Entry{Target: target, Date: date, Text: text}, false, false
}

// mentionedTitles finds shared note names appearing as plain words in a line that did
// not produce an entry. This is what turns "the timeline is empty" into "these four
// projects were written about without brackets".
func mentionedTitles(line string, sharedTitles map[string]bool) []string {
	// A line that opted out stays opted out; surfacing its subject would leak which
	// project a #private line was about.
	stripped := commentPattern.ReplaceAllString(line, "")
	if strings.TrimSpace(stripped) == "" || hasPrivateTag(stripped) {
		return nil
	}
	lower := strings.ToLower(stripped)
	linked := map[string]bool{}
	for _, existing := range vault.WikiLinks(stripped) {
		linked[strings.ToLower(existing)] = true
	}

	var found []string
	for title := range sharedTitles {
		if title == "" || linked[strings.ToLower(title)] {
			continue
		}
		if containsWord(lower, strings.ToLower(title)) {
			found = append(found, title)
		}
	}
	sort.Strings(found)
	return found
}

// containsWord matches a title only at word boundaries, so a project called "Go" is not
// found inside "going".
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for offset := 0; ; {
		index := strings.Index(haystack[offset:], needle)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(needle)
		beforeOK := start == 0 || !isWordByte(haystack[start-1])
		afterOK := end == len(haystack) || !isWordByte(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		offset = start + 1
		if offset >= len(haystack) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// hasPrivateTag reports whether the line opts out with #private, as a whole tag rather
// than a substring, so #privateKeyNotes is not mistaken for it.
func hasPrivateTag(text string) bool {
	for _, field := range strings.Fields(text) {
		field = strings.Trim(field, ".,;:!?()[]")
		if strings.EqualFold(field, "#private") || strings.EqualFold(field, "#nolog") {
			return true
		}
	}
	return false
}

// noteDate prefers the date in the filename, since daily notes are named for their day.
// A `date:` field is the next best thing, then mtime.
func noteDate(path string, fields map[string]string, info os.FileInfo) string {
	if match := datePattern.FindString(filepath.Base(path)); match != "" {
		return match
	}
	if match := datePattern.FindString(fields["date"]); match != "" {
		return match
	}
	return info.ModTime().Format("2006-01-02")
}

// Render writes the timeline as a generated note, newest first within each project.
func Render(settings *config.Config, result *Result, limitPerTarget int) (string, error) {
	byTarget := map[string][]Entry{}
	for _, entry := range result.Entries {
		byTarget[entry.Target] = append(byTarget[entry.Target], entry)
	}
	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	var out strings.Builder
	out.WriteString("---\ntype: reference\nstatus: evergreen\ntags: [meta, generated]\n---\n")
	out.WriteString("# Activity\n\n")
	out.WriteString("Per-project timeline, derived from the daily journal. **Do not edit by hand.**\n")
	out.WriteString("Regenerate with `aimem refresh`.\n\n")
	out.WriteString("Only journal lines that link to a note agents can already see appear here.\n")
	out.WriteString("To keep a line out, tag it `#private` or wrap it in `%%comments%%`.\n")
	out.WriteString("Everything below is readable by any agent with vault access.\n\n")
	out.WriteString(fmt.Sprintf("Generated: %s  \nProjects with activity: %d  \nEntries: %d\n\n",
		time.Now().Format("2006-01-02 15:04"), len(targets), len(result.Entries)))

	for _, target := range targets {
		entries := byTarget[target]
		out.WriteString(fmt.Sprintf("## [[%s]]\n\n", target))
		shown := entries
		if limitPerTarget > 0 && len(shown) > limitPerTarget {
			shown = shown[:limitPerTarget]
		}
		for _, entry := range shown {
			out.WriteString(fmt.Sprintf("- %s: %s\n", entry.Date, entry.Text))
		}
		if len(entries) > len(shown) {
			out.WriteString(fmt.Sprintf("- ... %d older\n", len(entries)-len(shown)))
		}
		out.WriteString("\n")
	}

	target := settings.MetaPath("Activity.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return target, err
	}
	return target, os.WriteFile(target, []byte(out.String()), 0o644)
}

// LastTouched maps a note title to the most recent journal date mentioning it. This is a
// second staleness signal: a project can be worked on without its note being edited.
func LastTouched(result *Result) map[string]string {
	latest := map[string]string{}
	for _, entry := range result.Entries {
		if entry.Date > latest[entry.Target] {
			latest[entry.Target] = entry.Date
		}
	}
	return latest
}
