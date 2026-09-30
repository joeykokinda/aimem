// Package journal appends linked lines to the daily note.
//
// # WHY THIS WRITES INTO A PRIVATE FOLDER
//
// The activity bridge only emits journal lines that link to an already-shared note. That
// rule is what makes the bridge safe, and it is also why the bridge sits idle in
// practice: it requires remembering to type [[Brackets]] at the exact moment you are
// least inclined to, which is while finishing something else.
//
// This closes that gap from the other side. `aimem log "deposit sweep works"` finds the
// project names in the sentence and writes the wikilinks for you, so the habit costs one
// command instead of a syntax you have to remember.
//
// The write happens only from the CLI, where a human typed the sentence. It is
// deliberately not exposed over MCP: an agent that can write into the private journal
// could stage its own text for extraction on the next refresh, which would turn the
// bridge into a laundering channel. Nothing in this package is reachable from the
// server.
package journal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/vault"
)

// Entry reports what a log call wrote and whether it will survive the bridge.
type Entry struct {
	Path    string
	Line    string
	Linked  []string // shared notes the line now links to
	Created bool     // the daily note did not exist
}

// Reaches reports whether this line will appear in the generated timeline. A line with
// no link to a shared note is kept in the journal but never leaves it.
func (e *Entry) Reaches() bool { return len(e.Linked) > 0 }

// Append writes one line to today's daily note, linking any shared note it names.
//
// forced names a note to link explicitly, for the case where the sentence does not
// contain the note's title verbatim.
func Append(settings *config.Config, notes []*vault.Note, text, forced string, when time.Time) (*Entry, error) {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "- "))
	if text == "" {
		return nil, fmt.Errorf("nothing to log")
	}
	if settings.Journal == "" {
		return nil, fmt.Errorf("no journal folder is configured; set folders.journal in %s", config.FileName)
	}
	// Same rule the bridge enforces on the way out, checked on the way in: the journal
	// must be a private folder, or this is writing user prose somewhere agents can read.
	if !settings.IsPrivate(settings.Journal) {
		return nil, fmt.Errorf("journal folder %q is not in the private tier; refusing to write", settings.Journal)
	}
	// A logged line is prose a human typed, and it lands in a file that is committed.
	if name := vault.MatchSecret(text); name != "" {
		return nil, fmt.Errorf("refusing to write: this looks like a %s", name)
	}

	linked, body := link(text, notes, forced)

	directory := filepath.Join(settings.Root, settings.Journal)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, when.Format(settings.JournalFilename)+".md")

	entry := &Entry{Path: path, Line: "- " + body, Linked: linked}

	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		entry.Created = true
		header := fmt.Sprintf("---\ntype: journal\ndate: %s\n---\n# %s\n\n",
			when.Format("2006-01-02"), when.Format("2006-01-02"))
		return entry, os.WriteFile(path, []byte(header+entry.Line+"\n"), 0o644)
	case err != nil:
		return nil, err
	}

	updated := strings.TrimRight(string(existing), "\n") + "\n" + entry.Line + "\n"
	return entry, os.WriteFile(path, []byte(updated), 0o644)
}

// link wraps every shared note title the text mentions in wikilinks, longest title first
// so "Creou Discover" is matched before "Creou" and the shorter name does not carve up
// the longer one.
func link(text string, notes []*vault.Note, forced string) ([]string, string) {
	titles := make([]string, 0, len(notes))
	for _, note := range notes {
		if note.Title != "" && !strings.EqualFold(note.Title, "README") {
			titles = append(titles, note.Title)
		}
	}
	sort.Slice(titles, func(a, b int) bool { return len(titles[a]) > len(titles[b]) })

	var linked []string
	seen := map[string]bool{}

	// Text already carrying a link keeps it, and that link counts.
	for _, existing := range vault.WikiLinks(text) {
		for _, title := range titles {
			if strings.EqualFold(existing, title) && !seen[title] {
				seen[title] = true
				linked = append(linked, title)
			}
		}
	}

	for _, title := range titles {
		if seen[title] {
			continue
		}
		pattern, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(title) + `\b`)
		if err != nil {
			continue
		}
		replaced := false
		text = replaceOutsideLinks(text, pattern, func(match string) string {
			if replaced {
				return match
			}
			replaced = true
			// Keep what the user typed as the display text when the casing differs, so
			// the line still reads the way they wrote it.
			if match == title {
				return "[[" + title + "]]"
			}
			return "[[" + title + "|" + match + "]]"
		})
		if replaced {
			seen[title] = true
			linked = append(linked, title)
		}
	}

	if forced != "" && !seen[forced] {
		for _, title := range titles {
			if strings.EqualFold(title, forced) {
				text = "[[" + title + "]] " + text
				linked = append(linked, title)
				seen[title] = true
				break
			}
		}
	}

	sort.Strings(linked)
	return linked, text
}

// replaceOutsideLinks applies a replacement only to text that is not already inside a
// [[wikilink]], so linking "Creou" cannot corrupt an existing [[Creou Discover]].
func replaceOutsideLinks(text string, pattern *regexp.Regexp, replace func(string) string) string {
	var out strings.Builder
	rest := text
	for {
		open := strings.Index(rest, "[[")
		if open < 0 {
			out.WriteString(pattern.ReplaceAllStringFunc(rest, replace))
			return out.String()
		}
		closing := strings.Index(rest[open:], "]]")
		if closing < 0 {
			out.WriteString(pattern.ReplaceAllStringFunc(rest, replace))
			return out.String()
		}
		out.WriteString(pattern.ReplaceAllStringFunc(rest[:open], replace))
		out.WriteString(rest[open : open+closing+2])
		rest = rest[open+closing+2:]
	}
}

// Commit is one commit found in a mapped repository.
type Commit struct {
	SHA     string
	Subject string
	Note    *vault.Note
}

// FromGit collects commits you authored since a cutoff, across every repository a vault
// note claims.
//
// This is the automatic half of journaling. `aimem log` still needs you to type a
// sentence, and the things worth typing are the ones git cannot see: why you chose
// something, what you ruled out. But "which projects did I touch today" is already
// recorded, with timestamps, in repositories that already map to notes. Deriving it
// costs nothing and makes the timeline populate itself.
//
// Only commits by the given author count. A repository you contribute to alongside other
// people would otherwise fill your journal with their work.
func FromGit(notes []*vault.Note, author string, since time.Time) ([]Commit, error) {
	var found []Commit
	for _, note := range notes {
		if note.Repo == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(note.Repo, ".git")); err != nil || info == nil {
			continue
		}
		arguments := []string{
			"-C", note.Repo, "log",
			"--since=" + since.Format("2006-01-02T15:04:05"),
			"--no-merges", "--pretty=format:%h\x1f%s",
		}
		if author != "" {
			arguments = append(arguments, "--author="+author)
		}
		output, err := exec.Command("git", arguments...).Output()
		if err != nil {
			continue // a repo that cannot be read is not an error worth stopping for
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			sha, subject, ok := strings.Cut(line, "\x1f")
			if !ok || strings.TrimSpace(subject) == "" {
				continue
			}
			found = append(found, Commit{SHA: sha, Subject: strings.TrimSpace(subject), Note: note})
		}
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].Note.Title < found[b].Note.Title })
	return found, nil
}

// GitAuthor returns the email git would attribute commits to, which is the filter that
// keeps other people's commits out of your journal.
func GitAuthor(repo string) string {
	arguments := []string{"config", "user.email"}
	if repo != "" {
		arguments = append([]string{"-C", repo}, arguments...)
	}
	output, err := exec.Command("git", arguments...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// AppendCommits writes one journal line per commit, skipping any already recorded.
//
// Each line carries its short SHA, which is what makes this safe to run repeatedly: on
// a timer, from a shell hook, or twice by hand. The SHA is the dedupe key, so re-running
// adds only what is new.
func AppendCommits(settings *config.Config, commits []Commit, when time.Time) (int, string, error) {
	if settings.Journal == "" {
		return 0, "", fmt.Errorf("no journal folder is configured; set folders.journal in %s", config.FileName)
	}
	if !settings.IsPrivate(settings.Journal) {
		return 0, "", fmt.Errorf("journal folder %q is not in the private tier; refusing to write", settings.Journal)
	}

	directory := filepath.Join(settings.Root, settings.Journal)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return 0, "", err
	}
	path := filepath.Join(directory, when.Format(settings.JournalFilename)+".md")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, path, err
	}
	body := string(existing)
	if body == "" {
		body = fmt.Sprintf("---\ntype: journal\ndate: %s\n---\n# %s\n",
			when.Format("2006-01-02"), when.Format("2006-01-02"))
	}

	var added int
	var lines []string
	for _, commit := range commits {
		if strings.Contains(body, "("+commit.SHA+")") {
			continue
		}
		// A commit subject is text the author wrote, and it lands in a committed file.
		if vault.MatchSecret(commit.Subject) != "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- [[%s]] %s (%s)", commit.Note.Title, commit.Subject, commit.SHA))
		added++
	}
	if added == 0 {
		return 0, path, nil
	}

	updated := strings.TrimRight(body, "\n") + "\n" + strings.Join(lines, "\n") + "\n"
	return added, path, os.WriteFile(path, []byte(updated), 0o644)
}
