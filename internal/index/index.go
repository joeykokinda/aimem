// Package index renders the generated context files an agent reads: BRAIN.md, the
// cross-project index, and the machine-readable JSON beside it.
//
// BRAIN.md is loaded at the start of every agent session, so its size is a cost paid on
// every turn. Entries are therefore tiered by status: live work is described in full,
// finished and abandoned work collapses to a name and a path. The index should stay
// roughly flat as the vault grows.
package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joeykokinda/aimem/internal/activity"
	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/profile"
	"github.com/joeykokinda/aimem/internal/vault"
)

// Brain is the machine-readable form of the index, for callers that want to filter
// programmatically instead of parsing Markdown.
type Brain struct {
	Generated string         `json:"generated"`
	Vault     string         `json:"vault"`
	Config    *config.Config `json:"config"`
	Notes     []*vault.Note  `json:"notes"`
}

// Stale reports whether the generated index is older than the newest note it claims to
// describe. Refresh is fast enough to run constantly, but nothing runs it automatically
// between vault commits, so the index can silently drift behind the notes. Callers use
// this to warn rather than to serve something they know is wrong.
func Stale(settings *config.Config) (bool, time.Time, error) {
	generated, err := os.Stat(settings.MetaPath("BRAIN.md"))
	if err != nil {
		return true, time.Time{}, err
	}

	var newest time.Time
	for _, folder := range settings.IndexFolders() {
		root := filepath.Join(settings.Root, folder)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // a missing shared folder is the validator's problem, not ours
			}
			if !info.IsDir() && strings.HasSuffix(path, ".md") && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			return nil
		})
		if err != nil {
			return false, newest, err
		}
	}
	return newest.After(generated.ModTime()), newest, nil
}

// Result reports what a build produced, in counts only, so it is safe to print in front
// of an agent even though the journal bridge read private files to produce it.
type Result struct {
	BrainPath    string
	ActivityPath string
	JSONPath     string
	Notes        int
	Bytes        int
	Journal      *activity.Result

	// Counts of agent-written content awaiting review. Reported so the quarantine is
	// visible: a queue nobody is told about is just a place things go to be forgotten.
	PendingNotes int
	PendingFacts int
}

// Build regenerates every derived artifact from the vault: the journal timeline, the
// Markdown index, and optionally the JSON index. It does not validate; callers run the
// validator first, because regenerating from a broken vault propagates the breakage into
// every agent's context at once.
func Build(settings *config.Config, jsonPath string, skipJournal bool) (*Result, error) {
	notes, err := vault.Collect(settings, settings.IndexFolders())
	if err != nil {
		return nil, err
	}
	vault.ApplyGitDates(settings.Root, notes)

	result := &Result{Notes: len(notes)}
	for _, note := range notes {
		if note.Unreviewed() {
			result.PendingNotes++
		}
		result.PendingFacts += len(note.UnreviewedFacts())
	}
	lastTouched := map[string]string{}
	if !skipJournal {
		titles := map[string]bool{}
		for _, note := range notes {
			titles[note.Title] = true
		}
		journal, err := activity.Extract(settings, titles)
		if err != nil {
			return nil, err
		}
		path, err := activity.Render(settings, journal, settings.JournalEntriesPerProject)
		if err != nil {
			return nil, err
		}
		lastTouched = activity.LastTouched(journal)
		result.Journal = journal
		result.ActivityPath = path
	}

	markdown := render(settings, notes, lastTouched)
	target := settings.MetaPath("BRAIN.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(target, []byte(markdown), 0o644); err != nil {
		return nil, err
	}
	result.BrainPath = target
	result.Bytes = len(markdown)

	if jsonPath != "" {
		encoded, err := json.MarshalIndent(Brain{today(), settings.Root, settings, notes}, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(jsonPath, append(encoded, '\n'), 0o644); err != nil {
			return nil, err
		}
		result.JSONPath = jsonPath
	}
	return result, nil
}

// effectiveAge is days since the note last showed any sign of life, counting both edits
// to the note and journal mentions of it. A project can be worked on for weeks without
// its note being touched, and that is not the same thing as abandoned.
func effectiveAge(note *vault.Note, lastTouched map[string]string) int {
	age := note.AgeDays
	if journal, ok := lastTouched[note.Title]; ok {
		if parsed, err := time.Parse("2006-01-02", journal); err == nil {
			if days := int(time.Since(parsed).Hours() / 24); days < age {
				age = days
			}
		}
	}
	return age
}

func render(settings *config.Config, notes []*vault.Note, lastTouched map[string]string) string {
	var out strings.Builder

	out.WriteString("---\ntype: reference\nstatus: evergreen\ntags: [meta, generated]\n---\n")
	out.WriteString("# BRAIN\n\n")
	out.WriteString("Generated index of the shared half of this vault. **Do not edit by hand.**\n")
	out.WriteString("Regenerate with `aimem refresh`.\n\n")
	out.WriteString("This is the file an AI agent should read first. It covers every company,\n")
	out.WriteString("project, research thread, and idea, with the repo path for each.\n\n")
	out.WriteString(fmt.Sprintf("Private folders (%s) and the encrypted `%s` section are excluded by\n",
		backticked(settings.Private), settings.Locked))
	out.WriteString("design and are not summarized here.\n\n")
	out.WriteString("Live work is described in full. Shipped, dead, and paused work is listed by\n")
	out.WriteString("name only; open its note for detail. Recent per-project history is in\n")
	out.WriteString("[[Activity]].\n\n")
	out.WriteString(fmt.Sprintf("Vault: `%s`  \nGenerated: %s  \nIndexed notes: %d\n\n", settings.Root, today(), len(notes)))

	// Agent-written notes are held out of every section below until a human promotes
	// them. This is the whole junk-control story: an unreviewed note costs nothing
	// because it is not in the file each session reads, so agents can write freely and
	// triage can wait.
	var reviewed, pending []*vault.Note
	for _, note := range notes {
		if note.Unreviewed() {
			pending = append(pending, note)
			continue
		}
		reviewed = append(reviewed, note)
	}
	notes = reviewed

	writeUnreviewed(&out, pending)
	writeProfile(&out, profile.Build(settings, notes, lastTouched))
	writeAttention(&out, settings, notes, lastTouched)
	writeCompanies(&out, notes)
	writeProjects(&out, settings, notes, lastTouched)
	writeResearch(&out, notes)
	writeIdeas(&out, notes)
	writeReference(&out, notes)
	if settings.IndexRepoMap {
		writeRepoMap(&out, notes)
	} else {
		writeRepoPointer(&out, notes)
	}
	writeOrphans(&out, notes)

	return out.String()
}

// writeUnreviewed names what is waiting without describing it. A count and a list of
// titles is enough to decide whether to run `aimem review`; summarizing the content here
// would reintroduce exactly the per-session cost the quarantine exists to avoid.
func writeUnreviewed(out *strings.Builder, pending []*vault.Note) {
	if len(pending) == 0 {
		return
	}
	out.WriteString(fmt.Sprintf("## Unreviewed (%d)\n\n", len(pending)))
	out.WriteString("Written by an agent and not promoted yet, so they are deliberately not\n")
	out.WriteString("described here. Triage with `aimem review`.\n\n")
	var names []string
	for _, note := range pending {
		names = append(names, fmt.Sprintf("%s (`%s`)", note.Title, note.Path))
	}
	sort.Strings(names)
	out.WriteString(strings.Join(names, ", ") + "\n\n")
}

// writeProfile leads with who this person is and what they are doing right now, because
// it is the context an agent needs before it needs any particular project. It is derived
// on every refresh, so unlike a hand-written "about me" it cannot be out of date.
func writeProfile(out *strings.Builder, derived *profile.Profile) {
	if len(derived.Languages) == 0 && len(derived.Subjects) == 0 {
		return
	}
	out.WriteString("## Profile\n\n")
	out.WriteString("Derived on every refresh from the repos these notes claim and the tags on\n")
	out.WriteString("notes that have been touched recently. Not hand-maintained, so not stale.\n\n")

	if len(derived.ActiveNow) > 0 {
		out.WriteString(fmt.Sprintf("**Working on now:** %s\n\n", strings.Join(derived.ActiveNow, ", ")))
	}
	if top := topNames(derived.Languages, 6); len(top) > 0 {
		out.WriteString(fmt.Sprintf("**Writes:** %s  \n", strings.Join(top, ", ")))
		out.WriteString(fmt.Sprintf("*(from %d checkouts on disk)*\n\n", derived.ReposWalked))
	}
	if top := topNames(derived.Subjects, 10); len(top) > 0 {
		out.WriteString(fmt.Sprintf("**Subjects:** %s\n\n", strings.Join(top, ", ")))
	}
	if top := topNames(derived.Companies, 5); len(top) > 0 {
		out.WriteString(fmt.Sprintf("**Companies:** %s\n\n", strings.Join(top, ", ")))
	}
	if len(derived.Capability) > 0 {
		shown := derived.Capability
		if len(shown) > 8 {
			shown = shown[:8]
		}
		out.WriteString("**On hand** (accounts, hosts, hardware), recorded by hand because it cannot\n")
		out.WriteString("be derived: " + strings.Join(shown, ", ") + "\n\n")
	}
	if len(derived.Clusters) > 0 {
		out.WriteString("**Related work.** Notes sharing a subject, so a lesson from one probably\n")
		out.WriteString("applies to the others:\n\n")
		for _, cluster := range derived.Clusters {
			// Named, then truncated. A cluster's value is knowing the theme exists and
			// where to start; `aimem find --tag <reason>` returns the whole set, and
			// printing it here would be paid for on every session.
			shown := cluster.Notes
			overflow := 0
			if len(shown) > 6 {
				overflow = len(shown) - 6
				shown = shown[:6]
			}
			out.WriteString(fmt.Sprintf("- `%s`: %s", cluster.Reason, strings.Join(shown, ", ")))
			if overflow > 0 {
				out.WriteString(fmt.Sprintf(", +%d more (`aimem find --tag %s`)", overflow, cluster.Reason))
			}
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
}

// topNames renders the leading entries with their live-vs-total evidence, so a reader can
// tell current work from history.
func topNames(items []profile.Weighted, limit int) []string {
	var out []string
	for index, item := range items {
		if index >= limit {
			break
		}
		if item.Active > 0 && item.Active != item.Count {
			out = append(out, fmt.Sprintf("%s (%d, %d active)", item.Name, item.Count, item.Active))
			continue
		}
		out = append(out, fmt.Sprintf("%s (%d)", item.Name, item.Count))
	}
	return out
}

func byType(notes []*vault.Note, kind string) []*vault.Note {
	var matched []*vault.Note
	for _, note := range notes {
		if note.Type == kind {
			matched = append(matched, note)
		}
	}
	return matched
}

func skippable(note *vault.Note) bool {
	return strings.HasSuffix(note.Path, "README.md") || note.Title == "BRAIN" ||
		note.Title == "Activity" || note.Type == "dashboard"
}

// writeAttention leads with what is wrong, because an index nobody acts on is decoration.
// Stale actives are the important case: `status: active` is only a useful field if it can
// be falsified, and an untouched month falsifies it.
func writeAttention(out *strings.Builder, settings *config.Config, notes []*vault.Note, lastTouched map[string]string) {
	var stale, missing []string
	for _, note := range notes {
		if skippable(note) {
			continue
		}
		// Same rule as the validator's stale check, and for the same reason: only
		// project and company notes make a claim that silence can falsify. Reaching for
		// settings.StaleableTypes rather than restating the list keeps the two from drifting.
		if note.Status == "active" && vault.Contains(settings.StaleableTypes, note.Type) {
			if age := effectiveAge(note, lastTouched); age > settings.StaleDays {
				stale = append(stale, fmt.Sprintf("- **%s** %dd untouched - `%s`", note.Title, age, note.Path))
			}
		}
		if note.Repo != "" && !note.RepoOK {
			missing = append(missing, fmt.Sprintf("- **%s** repo `%s` is gone - `%s`", note.Title, note.Repo, note.Path))
		}
	}
	if len(stale) == 0 && len(missing) == 0 {
		return
	}

	out.WriteString("## Needs Attention\n\n")
	if len(stale) > 0 {
		out.WriteString(fmt.Sprintf("Claiming `status: active` but untouched for over %d days, counting both\n", settings.StaleDays))
		out.WriteString("note edits and journal mentions. Either work on it or change the status.\n\n")
		sort.Strings(stale)
		out.WriteString(strings.Join(stale, "\n") + "\n\n")
	}
	if len(missing) > 0 {
		out.WriteString("Notes pointing at a repo that is no longer on disk.\n\n")
		sort.Strings(missing)
		out.WriteString(strings.Join(missing, "\n") + "\n\n")
	}
}

func writeCompanies(out *strings.Builder, notes []*vault.Note) {
	companies := byType(notes, "company")
	if len(companies) == 0 {
		return
	}
	out.WriteString("## Companies\n\n")

	var retired []*vault.Note
	for _, company := range companies {
		if company.Status == "dead" || company.Status == "shipped" {
			retired = append(retired, company)
			continue
		}
		out.WriteString(fmt.Sprintf("### %s", company.Title))
		if company.Status != "" {
			out.WriteString(fmt.Sprintf(" (%s)", company.Status))
		}
		out.WriteString("\n")
		if company.Summary != "" {
			out.WriteString(company.Summary + "\n")
		}
		out.WriteString(fmt.Sprintf("- note: `%s`\n", company.Path))
		if company.Repo != "" {
			out.WriteString(fmt.Sprintf("- repo: `%s`%s\n", company.Repo, missingMarker(company)))
		}
		var children []string
		for _, note := range notes {
			if note.Company == company.Title && note.Type == "project" {
				children = append(children, note.Title)
			}
		}
		if len(children) > 0 {
			out.WriteString(fmt.Sprintf("- projects: %s\n", strings.Join(children, ", ")))
		}
		out.WriteString("\n")
	}

	if len(retired) > 0 {
		out.WriteString("**Retired:** ")
		var names []string
		for _, company := range retired {
			names = append(names, fmt.Sprintf("%s (`%s`)", company.Title, company.Path))
		}
		out.WriteString(strings.Join(names, ", ") + "\n\n")
	}
}

// writeProjects describes active work in full and collapses everything else. Paused work
// keeps a one-line summary because it is the pool you pull from next; shipped and dead
// work keeps only a name, because the only question left is where its note lives.
func writeProjects(out *strings.Builder, settings *config.Config, notes []*vault.Note, lastTouched map[string]string) {
	projects := byType(notes, "project")
	if len(projects) == 0 {
		return
	}
	byStatus := map[string][]*vault.Note{}
	for _, project := range projects {
		if skippable(project) {
			continue
		}
		status := project.Status
		if status == "" {
			status = "unset"
		}
		byStatus[status] = append(byStatus[status], project)
	}

	out.WriteString("## Projects\n\n")

	for _, status := range []string{"active", "unset"} {
		group := byStatus[status]
		if len(group) == 0 {
			continue
		}
		out.WriteString(fmt.Sprintf("### %s (%d)\n\n", status, len(group)))
		for _, project := range group {
			out.WriteString(fmt.Sprintf("- **%s**", project.Title))
			if project.Company != "" {
				out.WriteString(fmt.Sprintf(" [%s]", project.Company))
			}
			if len(project.Tags) > 0 {
				out.WriteString(fmt.Sprintf(" `%s`", strings.Join(project.Tags, " ")))
			}
			if age := effectiveAge(project, lastTouched); age > settings.StaleDays {
				out.WriteString(fmt.Sprintf(" **(stale %dd)**", age))
			}
			out.WriteString("\n")
			if project.Summary != "" {
				out.WriteString(fmt.Sprintf("  - %s\n", project.Summary))
			}
			if project.Repo != "" {
				out.WriteString(fmt.Sprintf("  - repo: `%s`%s\n", project.Repo, missingMarker(project)))
			}
			out.WriteString(fmt.Sprintf("  - note: `%s`\n", project.Path))
		}
		out.WriteString("\n")
	}

	if group := byStatus["paused"]; len(group) > 0 {
		out.WriteString(fmt.Sprintf("### paused (%d)\n\n", len(group)))
		for _, project := range group {
			out.WriteString(fmt.Sprintf("- **%s**", project.Title))
			if project.Summary != "" {
				out.WriteString(": " + clipTo(project.Summary, 110))
			}
			out.WriteString(fmt.Sprintf(" - `%s`\n", project.Path))
		}
		out.WriteString("\n")
	}

	for _, status := range []string{"shipped", "evergreen", "dead"} {
		group := byStatus[status]
		if len(group) == 0 {
			continue
		}
		out.WriteString(fmt.Sprintf("### %s (%d)\n\n", status, len(group)))
		var names []string
		for _, project := range group {
			names = append(names, fmt.Sprintf("%s (`%s`)", project.Title, project.Path))
		}
		out.WriteString(strings.Join(names, ", ") + "\n\n")
	}
}

func writeResearch(out *strings.Builder, notes []*vault.Note) {
	group := byType(notes, "research")
	if len(group) == 0 {
		return
	}
	out.WriteString("## Research Threads\n\n")
	for _, note := range group {
		if skippable(note) {
			continue
		}
		out.WriteString(fmt.Sprintf("- **%s**", note.Title))
		if note.Summary != "" {
			out.WriteString(": " + clipTo(note.Summary, 110))
		}
		out.WriteString(fmt.Sprintf(" - `%s`\n", note.Path))
	}
	out.WriteString("\n")
}

// writeIdeas keeps near-term ideas legible and reduces the rest to names. An idea parked
// on a far horizon needs to be findable, not summarized.
func writeIdeas(out *strings.Builder, notes []*vault.Note) {
	ideas := byType(notes, "idea")
	if len(ideas) == 0 {
		return
	}
	out.WriteString("## Ideas\n\n")

	var near, far []*vault.Note
	for _, idea := range ideas {
		if skippable(idea) {
			continue
		}
		if idea.Status == "dead" || strings.EqualFold(idea.Horizon, "future") {
			far = append(far, idea)
			continue
		}
		near = append(near, idea)
	}

	for _, idea := range near {
		horizon := idea.Horizon
		if horizon == "" {
			horizon = "unset"
		}
		out.WriteString(fmt.Sprintf("- **%s** (%s)", idea.Title, horizon))
		if idea.Summary != "" {
			out.WriteString(": " + clipTo(idea.Summary, 120))
		}
		out.WriteString(fmt.Sprintf(" - `%s`\n", idea.Path))
	}
	if len(far) > 0 {
		out.WriteString("\n**Parked:** ")
		var names []string
		for _, idea := range far {
			names = append(names, fmt.Sprintf("%s (`%s`)", idea.Title, idea.Path))
		}
		out.WriteString(strings.Join(names, ", ") + "\n")
	}
	out.WriteString("\n")
}

func writeReference(out *strings.Builder, notes []*vault.Note) {
	group := byType(notes, "reference")
	if len(group) == 0 {
		return
	}
	out.WriteString("## Reference\n\n")
	out.WriteString("Durable how-to and spec notes. Read one before re-deriving what it covers.\n")
	out.WriteString("Listed by name only on purpose: a reference note is a pointer, and\n")
	out.WriteString("summarizing it here would mean carrying it in every session's context.\n\n")
	var names []string
	for _, note := range group {
		if skippable(note) {
			continue
		}
		names = append(names, fmt.Sprintf("%s (`%s`)", note.Title, note.Path))
	}
	out.WriteString(strings.Join(names, ", ") + "\n\n")
}

// writeRepoPointer replaces the repo table with a count and the commands that answer the
// question precisely. The table is the largest fixed block in the file and is read end to
// end by nobody; an agent standing in a checkout wants one answer, not forty rows.
func writeRepoPointer(out *strings.Builder, notes []*vault.Note) {
	mapped := 0
	for _, note := range notes {
		if note.Repo != "" {
			mapped++
		}
	}
	if mapped == 0 {
		return
	}
	out.WriteString("## Repo Map\n\n")
	out.WriteString(fmt.Sprintf("%d notes map to a checkout on disk. Rather than listing them here, ask:\n", mapped))
	out.WriteString("`aimem project` for the repo you are in, or `aimem context --json` for all of them.\n\n")
}

func writeRepoMap(out *strings.Builder, notes []*vault.Note) {
	type entry struct {
		repo string
		note *vault.Note
	}
	var entries []entry
	for _, note := range notes {
		if note.Repo != "" {
			entries = append(entries, entry{note.Repo, note})
		}
	}
	if len(entries) == 0 {
		return
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].repo < entries[b].repo })
	out.WriteString("## Repo Map\n\n")
	out.WriteString("Reverse lookup: given a checkout on disk, which note describes it.\n\n")
	out.WriteString("| repo | note |\n|---|---|\n")
	for _, item := range entries {
		flag := ""
		if !item.note.RepoOK {
			flag = " **(missing)**"
		}
		out.WriteString(fmt.Sprintf("| `%s` | `%s`%s |\n", item.repo, item.note.Path, flag))
	}
	out.WriteString("\n")
}

func writeOrphans(out *strings.Builder, notes []*vault.Note) {
	linked := map[string]bool{}
	for _, note := range notes {
		for _, link := range note.Links {
			linked[link] = true
		}
	}
	var orphans []string
	for _, note := range notes {
		if skippable(note) {
			continue
		}
		if !linked[note.Title] {
			orphans = append(orphans, note.Title)
		}
	}
	if len(orphans) == 0 {
		return
	}
	out.WriteString("## Unlinked Notes\n\n")
	out.WriteString("Nothing in the shared vault links to these, so the graph cannot reach them.\n")
	out.WriteString("Either wire them into a related note or delete them.\n\n")
	out.WriteString(strings.Join(orphans, ", ") + "\n\n")
}

func missingMarker(note *vault.Note) string {
	if note.RepoOK {
		return ""
	}
	return " (not on disk)"
}

func clipTo(text string, limit int) string {
	if len(text) > limit {
		return strings.TrimSpace(text[:limit]) + "..."
	}
	return text
}

func today() string { return time.Now().Format("2006-01-02 15:04") }

// backticked renders a folder list for prose, so the generated header names this vault's
// actual private folders rather than a hardcoded example.
func backticked(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	return strings.Join(quoted, ", ")
}
