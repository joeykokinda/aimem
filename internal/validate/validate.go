// Package validate checks a vault against the contract in its config, so schema drift
// gets caught by a tool instead of by someone reading ninety notes.
//
// Every rule here is config-driven. The checks that matter most are the ones guarding
// the privacy boundary: a leaked secret and an un-ignored locked folder are errors, not
// warnings, because the vault is pushed to a remote.
package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/vault"
)

// Problem is one finding. Errors gate a refresh; warnings are advisory unless strict.
type Problem struct {
	Level string `json:"level"` // "error" or "warn"
	Check string `json:"check"`
	Path  string `json:"path"`
	Line  int    `json:"line,omitempty"`
	Msg   string `json:"message"`
}

type problem = Problem

// Report is the outcome of a full run.
type Report struct {
	Problems []Problem `json:"problems"`
	Notes    int       `json:"notes"`
	Errors   int       `json:"errors"`
	Warnings int       `json:"warnings"`
}

// OK reports whether the run passed. Warnings only fail under strict.
func (r *Report) OK(strict bool) bool {
	return r.Errors == 0 && !(strict && r.Warnings > 0)
}

// Run executes every check against the readable half of the vault. Private folders are
// never walked; the link checker indexes private note *names* so links to them resolve,
// but opens no private file.
func Run(settings *config.Config) (*Report, error) {
	notes, err := vault.Collect(settings, settings.ValidateFolders())
	if err != nil {
		return nil, err
	}
	vault.ApplyGitDates(settings.Root, notes)

	var problems []Problem
	problems = append(problems, checkConfig(settings)...)
	problems = append(problems, checkFrontmatter(settings, notes)...)
	problems = append(problems, checkStale(settings, notes)...)
	problems = append(problems, checkRepos(settings, notes)...)
	problems = append(problems, checkLinks(settings, notes)...)
	problems = append(problems, checkDuplicateTitles(settings, notes)...)
	problems = append(problems, checkSecrets(notes)...)
	problems = append(problems, checkFolderTagging(settings, notes)...)
	problems = append(problems, checkNonMarkdown(settings)...)
	problems = append(problems, checkLockedNotCommitted(settings)...)

	sort.SliceStable(problems, func(a, b int) bool {
		if problems[a].Level != problems[b].Level {
			return problems[a].Level == "error"
		}
		return problems[a].Path < problems[b].Path
	})

	report := &Report{Problems: problems, Notes: len(notes)}
	for _, item := range problems {
		if item.Level == "error" {
			report.Errors++
		} else {
			report.Warnings++
		}
	}
	return report, nil
}

// checkConfig catches vault folders that exist on disk but are in no tier. An unlisted
// folder is not private, it is invisible: nothing indexes it and nothing warns about it,
// so notes quietly stop being reachable.
func checkConfig(settings *config.Config) []Problem {
	entries, err := os.ReadDir(settings.Root)
	if err != nil {
		return nil
	}
	var found []Problem
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if settings.IsShared(name) || settings.IsPrivate(name) {
			continue
		}
		found = append(found, Problem{"warn", "untiered-folder", name, 0,
			fmt.Sprintf("%q is in no tier in %s; it is neither indexed nor protected", name, config.FileName)})
	}
	return found
}

func checkFrontmatter(settings *config.Config, notes []*vault.Note) []problem {
	var found []problem
	for _, note := range notes {
		if isTemplate(settings, note.Path) {
			continue
		}
		if !vault.HasFrontmatter(note.Body) {
			found = append(found, problem{"error", "missing-frontmatter", note.Path, 1,
				"no --- frontmatter block; dashboards and BRAIN.md cannot see this note"})
			continue
		}
		if note.Type == "" {
			found = append(found, problem{"error", "missing-type", note.Path, 2,
				"no `type:` field"})
		} else if !vault.Contains(settings.Types, note.Type) {
			found = append(found, problem{"error", "invalid-type", note.Path, 2,
				fmt.Sprintf("type %q is not one of: %s", note.Type, strings.Join(settings.Types, " "))})
		}

		// `updated:` is derived from git now. A hand-kept copy has no way to stay
		// correct: it only changes when someone remembers, so the notes that most need
		// an accurate date are exactly the ones whose date is most wrong.
		if _, present := note.Fields["updated"]; present {
			found = append(found, problem{"warn", "manual-updated", note.Path, 0,
				fmt.Sprintf("remove `updated:`; git says this note last changed %s", note.Updated)})
		}

		// Journals carry a date instead of a status; everything else needs one.
		if note.Type != "journal" {
			if note.Status == "" {
				found = append(found, problem{"error", "missing-status", note.Path, 3,
					"no `status:` field; staleness and dashboards cannot see this note"})
			} else if !vault.Contains(settings.Statuses, note.Status) {
				found = append(found, problem{"error", "invalid-status", note.Path, 3,
					fmt.Sprintf("status %q is not one of: %s", note.Status, strings.Join(settings.Statuses, " "))})
			}
		}
	}
	return found
}

// checkStale warns when a note claims to be active but git says nobody has touched it
// for a month. This is the check that keeps `status:` honest. Without it every note stays
// active forever, the field stops carrying information, and the dashboards that query it
// return everything.
//
// Journal mentions are not counted here: the validator stays inside the shared folders,
// and BRAIN.md is where the two signals get combined.
func checkStale(settings *config.Config, notes []*vault.Note) []problem {
	var found []problem
	for _, note := range notes {
		if isTemplate(settings, note.Path) || note.Title == "BRAIN" || note.Title == "Activity" {
			continue
		}
		if note.IsStale(settings) {
			found = append(found, problem{"warn", "stale-active", note.Path, 3,
				fmt.Sprintf("status is active but last commit was %s (%d days ago); mark it paused or work on it",
					note.Updated, note.AgeDays)})
		}
	}
	return found
}

func checkRepos(settings *config.Config, notes []*vault.Note) []problem {
	var found []problem
	for _, note := range notes {
		if note.Repo == "" {
			if (note.Type == "project" || note.Type == "company") &&
				note.Status != "dead" && !isTemplate(settings, note.Path) {
				found = append(found, problem{"warn", "no-repo", note.Path, 0,
					"project/company note has no `repo:` field"})
			}
			continue
		}
		// An absolute path names exactly one machine. That used to be required; it is now
		// the least portable option, because a vault synced to a second machine with a
		// different username or layout resolves every one of them to nothing.
		if config.IsRootedPath(note.RepoRaw) {
			found = append(found, problem{"warn", "absolute-repo", note.Path, 0,
				fmt.Sprintf("repo %q is absolute and will not resolve on another machine; run `aimem portable` to rewrite it",
					note.RepoRaw)})
		}
		if !note.RepoOK {
			found = append(found, problem{"warn", "missing-repo", note.Path, 0,
				fmt.Sprintf("repo %q resolves to %q, which does not exist on disk", note.RepoRaw, note.Repo)})
		}
	}
	return found
}

// checkLinks resolves every wikilink against note titles and vault-relative paths.
// Obsidian resolves by basename, so a bare [[Title]] matching any note is fine.
func checkLinks(settings *config.Config, notes []*vault.Note) []problem {
	root := settings.Root
	titles := map[string]bool{}
	paths := map[string]bool{}
	for _, note := range notes {
		titles[note.Title] = true
		paths[strings.TrimSuffix(note.Path, ".md")] = true
	}
	// Attachments are valid Obsidian link targets too. Index their names and paths
	// without opening them, while preserving the shared-folder privacy boundary.
	folders := settings.ValidateFolders()
	for _, folder := range folders {
		filepath.Walk(filepath.Join(root, folder), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || strings.EqualFold(filepath.Ext(path), ".md") {
				return nil
			}
			titles[filepath.Base(path)] = true
			if relative, err := filepath.Rel(root, path); err == nil {
				paths[relative] = true
			}
			return nil
		})
	}
	// Private notes are not collected but are still valid link targets, so index their
	// names and paths only. No file is opened and no content is read.
	for _, folder := range settings.Private {
		filepath.Walk(filepath.Join(root, folder), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			titles[strings.TrimSuffix(filepath.Base(path), ".md")] = true
			if relative, err := filepath.Rel(root, path); err == nil {
				paths[strings.TrimSuffix(relative, ".md")] = true
			}
			return nil
		})
	}

	var found []problem
	for _, note := range notes {
		if isTemplate(settings, note.Path) {
			continue
		}
		for _, link := range note.Links {
			if titles[link] || paths[link] || paths[strings.TrimSuffix(link, ".md")] {
				continue
			}
			found = append(found, problem{"error", "broken-link", note.Path, lineOf(note, "[["+link),
				fmt.Sprintf("[[%s]] resolves to nothing", link)})
		}
	}
	return found
}

func checkDuplicateTitles(settings *config.Config, notes []*vault.Note) []problem {
	seen := map[string][]string{}
	for _, note := range notes {
		// One README per folder is the convention, not a collision.
		if isTemplate(settings, note.Path) || note.Title == "README" {
			continue
		}
		seen[note.Title] = append(seen[note.Title], note.Path)
	}
	var found []problem
	for title, paths := range seen {
		if len(paths) > 1 {
			sort.Strings(paths)
			found = append(found, problem{"warn", "duplicate-title", paths[0], 0,
				fmt.Sprintf("%d notes named %q (%s); bare [[%s]] links are ambiguous",
					len(paths), title, strings.Join(paths, ", "), title)})
		}
	}
	return found
}

func checkSecrets(notes []*vault.Note) []problem {
	var found []problem
	for _, note := range notes {
		for index, line := range note.Lines {
			if name := vault.MatchSecret(line); name != "" {
				found = append(found, problem{"error", "possible-secret", note.Path, index + 1,
					fmt.Sprintf("looks like a %s; the vault is pushed to a remote, so secrets must not be committed", name)})
			}
		}
	}
	return found
}

// Vault rule 5: folders for where, tags for what. Do not tag a note with its own folder.
func checkFolderTagging(settings *config.Config, notes []*vault.Note) []problem {
	var found []problem
	for _, note := range notes {
		// 99-Meta is scaffolding, and `meta` is the tag that identifies it as such.
		if strings.HasPrefix(note.Path, settings.Meta) {
			continue
		}
		parts := strings.Split(filepath.Dir(note.Path), string(filepath.Separator))
		for _, part := range parts {
			bare := strings.ToLower(strings.TrimLeft(part, "0123456789-"))
			if bare == "" || bare == "." {
				continue
			}
			for _, tag := range note.Tags {
				if strings.ToLower(tag) == bare || strings.ToLower(tag)+"s" == bare {
					found = append(found, problem{"warn", "folder-tag", note.Path, 0,
						fmt.Sprintf("tagged %q inside folder %q; folders say where, tags say what", tag, part)})
				}
			}
		}
	}
	return found
}

// Vault rule 3: no code in the vault.
func checkNonMarkdown(settings *config.Config) []problem {
	root := settings.Root
	var found []problem
	codeExtensions := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".sh": true, ".c": true, ".h": true, ".rs": true, ".sol": true, ".rb": true,
	}
	folders := settings.ValidateFolders()
	for _, folder := range folders {
		filepath.Walk(filepath.Join(root, folder), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if codeExtensions[strings.ToLower(filepath.Ext(path))] {
				relative, _ := filepath.Rel(root, path)
				found = append(found, problem{"warn", "code-in-vault", relative, 0,
					"code belongs in a repository, not the vault"})
			}
			return nil
		})
	}
	return found
}

// The locked folder holds plaintext only while mounted. If it is not gitignored, an
// auto-commit during an unlocked window would push that plaintext to GitHub.
func checkLockedNotCommitted(settings *config.Config) []problem {
	root := settings.Root
	if settings.Locked == "" {
		return nil
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	if strings.Contains(string(ignore), settings.Locked) {
		return nil
	}
	return []problem{{"error", "locked-not-ignored", ".gitignore", 0,
		fmt.Sprintf("%s/ is not gitignored; unlocked plaintext could be committed and pushed", settings.Locked)}}
}

func isTemplate(settings *config.Config, path string) bool {
	return strings.HasPrefix(path, filepath.Join(settings.Meta, "Templates"))
}

func lineOf(note *vault.Note, needle string) int {
	for index, line := range note.Lines {
		if strings.Contains(line, needle) {
			return index + 1
		}
	}
	return 0
}
