// Command aimem is local-first memory for people and coding agents.
//
// A Markdown vault is the human-facing store; aimem is the deterministic retrieval layer
// that gives agents useful context without exposing the whole vault. Everything it knows
// about a particular vault comes from a single `.aimem.yml` at that vault's root, so this
// binary is generic and the vault is self-describing.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/index"
	"github.com/joeykokinda/aimem/internal/mcp"
	"github.com/joeykokinda/aimem/internal/search"
	"github.com/joeykokinda/aimem/internal/sync"
	"github.com/joeykokinda/aimem/internal/validate"
	"github.com/joeykokinda/aimem/internal/vault"
)

// Version is overridden at build time with -ldflags "-X main.Version=...".
var Version = "dev"

const usage = `aimem - local-first memory for people and coding agents

Reading
  context [project]     The cross-project index, or one project in full
  find <words>          Ranked search across the shared vault
  show <note>           Print one note by title or path
  project               The vault note for the repo you are standing in
  path                  Just that note's path
  activity [project]    Timeline derived from the daily journal
  stale                 Notes claiming active that nobody has touched

Writing
  remember <fact>       Append a durable fact to this repo's note
  refresh               Validate, rebuild the index, sync repo context

Setup
  init                  Create a vault config (and folders) and build the index
  use <path>            Point this machine at an existing vault
  config                Print the resolved vault contract
  doctor                Check the install, the config, and the boundary
  validate              Check the vault against its config
  mcp                   Serve the vault to agents over MCP (read-only)
  install-hooks         Install the vault's pre-commit hook
  version               Print the version

Common flags
  --vault PATH          Vault root. Resolution order: --vault, $AIMEM_VAULT,
                        $OBBY_VAULT, the path saved by "aimem use", then ~/vault
  --json                Machine-readable output, where it makes sense

Private and locked folders are never read. See README.md for the contract.`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return
	}
	command := os.Args[1]
	args := os.Args[2:]

	var err error
	switch command {
	case "context":
		err = runContext(args)
	case "find", "search":
		err = runFind(args)
	case "show", "note":
		err = runShow(args)
	case "project":
		err = runProject(args, true)
	case "path":
		err = runProject(args, false)
	case "activity":
		err = runActivity(args)
	case "stale":
		err = runStale(args)
	case "remember":
		err = runRemember(args)
	case "refresh":
		err = runRefresh(args)
	case "validate":
		err = runValidate(args)
	case "init":
		err = runInit(args)
	case "use":
		err = runUse(args)
	case "config":
		err = runConfig(args)
	case "doctor":
		err = runDoctor(args)
	case "mcp":
		err = runMCP(args)
	case "install-hooks":
		err = runInstallHooks(args)
	case "version", "--version", "-v":
		fmt.Printf("aimem %s\n", Version)
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s\n", command, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// flags builds a flag set carrying the options every command shares.
type common struct {
	set   *flag.FlagSet
	vault *string
	json  *bool
}

func newFlags(name string) *common {
	set := flag.NewFlagSet(name, flag.ExitOnError)
	return &common{
		set:   set,
		vault: set.String("vault", config.DefaultRoot(), "vault root"),
		json:  set.Bool("json", false, "machine-readable output"),
	}
}

func (c *common) parse(args []string) (*config.Config, error) {
	if err := c.set.Parse(permute(c.set, args)); err != nil {
		return nil, err
	}
	return config.Load(*c.vault)
}

// permute moves flags ahead of positional arguments.
//
// Go's flag package stops parsing at the first non-flag argument, so `aimem find hedera
// --json` would treat "--json" as a third search term and quietly return nothing. Every
// command here takes positional arguments, so flags have to be accepted on either side
// of them, which is what users expect from every other search tool.
func permute(set *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]

		// Everything after a bare "--" is positional by definition, which is also how a
		// search term that starts with a dash gets through.
		if argument == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		if len(argument) < 2 || argument[0] != '-' {
			positional = append(positional, argument)
			continue
		}

		flags = append(flags, argument)
		name := strings.TrimLeft(argument, "-")
		if strings.Contains(name, "=") {
			continue // --flag=value is self-contained
		}
		// A boolean flag takes no value; anything else consumes the next argument.
		if found := set.Lookup(name); found != nil {
			if boolean, ok := found.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
				continue
			}
		}
		if index+1 < len(args) {
			index++
			flags = append(flags, args[index])
		}
	}
	if len(positional) == 0 {
		return flags
	}
	return append(append(flags, "--"), positional...)
}

// notes loads the shared half of the vault with git dates applied.
func notesFor(settings *config.Config) ([]*vault.Note, error) {
	notes, err := vault.Collect(settings, settings.IndexFolders())
	if err != nil {
		return nil, err
	}
	vault.ApplyGitDates(settings.Root, notes)
	return notes, nil
}

// statePath is where the JSON index lives: a state directory, not the source checkout.
// Generated data in a git working tree is something you end up gitignoring and then
// accidentally shipping.
func statePath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "brain.json"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "aimem", "brain.json")
}

func indexPath() string {
	if override := strings.TrimSpace(os.Getenv("AIMEM_INDEX")); override != "" {
		return override
	}
	return statePath()
}

func runContext(args []string) error {
	flags := newFlags("context")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}

	// A named project is the cheap path: one note instead of the whole index. This is the
	// difference between paying for 40 projects and paying for the one you are in.
	if target := strings.Join(flags.set.Args(), " "); target != "" {
		notes, err := notesFor(settings)
		if err != nil {
			return err
		}
		note := search.Resolve(notes, target)
		if note == nil {
			return fmt.Errorf("no note named %q; try: aimem find %s", target, target)
		}
		if *flags.json {
			return emitJSON(note)
		}
		fmt.Printf("# %s\n`%s`\n\n%s\n", note.Title, note.Path, note.Body)
		return nil
	}

	if *flags.json {
		notes, err := notesFor(settings)
		if err != nil {
			return err
		}
		return emitJSON(index.Brain{Vault: settings.Root, Config: settings, Notes: notes})
	}

	raw, err := os.ReadFile(settings.MetaPath("BRAIN.md"))
	if err != nil {
		return fmt.Errorf("no index yet; run: aimem refresh")
	}
	fmt.Print(string(raw))
	return nil
}

func runFind(args []string) error {
	flags := newFlags("find")
	kind := flags.set.String("type", "", "filter by frontmatter type")
	status := flags.set.String("status", "", "filter by status")
	company := flags.set.String("company", "", "filter by company")
	tag := flags.set.String("tag", "", "filter by tag")
	limit := flags.set.Int("limit", 10, "maximum results")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}

	terms := flags.set.Args()
	if len(terms) == 0 && *kind == "" && *status == "" && *company == "" && *tag == "" {
		return fmt.Errorf("usage: aimem find <words> [--type T] [--status S] [--company C] [--tag G]")
	}

	notes, err := notesFor(settings)
	if err != nil {
		return err
	}
	hits := search.Run(notes, search.Query{
		Terms: terms, Type: *kind, Status: *status, Company: *company,
		Tag: *tag, Limit: *limit, Context: 3,
	})
	if *flags.json {
		return emitJSON(hits)
	}
	if len(hits) == 0 {
		fmt.Println("No matches in the shared vault.")
		return nil
	}
	for _, hit := range hits {
		fmt.Printf("%s", hit.Title)
		if hit.Type != "" {
			fmt.Printf("  [%s", hit.Type)
			if hit.Status != "" {
				fmt.Printf("/%s", hit.Status)
			}
			fmt.Print("]")
		}
		fmt.Printf("  %s (%s)\n", hit.Path, hit.Reason)
		if hit.Summary != "" {
			fmt.Printf("    %s\n", hit.Summary)
		}
		for _, line := range hit.Lines {
			fmt.Printf("    %d: %s\n", line.Number, line.Text)
		}
		fmt.Println()
	}
	return nil
}

func runShow(args []string) error {
	flags := newFlags("show")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	target := strings.Join(flags.set.Args(), " ")
	if target == "" {
		return fmt.Errorf("usage: aimem show <note title or path>")
	}
	notes, err := notesFor(settings)
	if err != nil {
		return err
	}
	note := search.Resolve(notes, target)
	if note == nil {
		return fmt.Errorf("no note named %q; try: aimem find %s", target, target)
	}
	if *flags.json {
		return emitJSON(note)
	}
	fmt.Printf("# %s\n`%s`\n\n%s\n", note.Title, note.Path, note.Body)
	return nil
}

// repoNote resolves the repository the caller is standing in to its vault note.
func repoNote(settings *config.Config) (*vault.Note, error) {
	working, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root := working
	if output, err := exec.Command("git", "-C", working, "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(output))
	}
	notes, err := notesFor(settings)
	if err != nil {
		return nil, err
	}
	// Most specific wins. A note claiming a parent directory must not shadow the note
	// claiming the exact checkout, and a note with no repo at all must match nothing:
	// an empty Repo would make the prefix test HasPrefix(root, "/"), which is every path.
	var best *vault.Note
	for _, note := range notes {
		if note.Repo == "" {
			continue
		}
		if note.Repo != root && !strings.HasPrefix(root, note.Repo+string(filepath.Separator)) {
			continue
		}
		if best == nil || len(note.Repo) > len(best.Repo) {
			best = note
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, fmt.Errorf("no vault note maps to %s\nAdd a `repo:` field to a note, then run: aimem refresh", root)
}

func runProject(args []string, full bool) error {
	flags := newFlags("project")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	note, err := repoNote(settings)
	if err != nil {
		return err
	}
	if *flags.json {
		return emitJSON(note)
	}
	if !full {
		fmt.Println(filepath.Join(settings.Root, note.Path))
		return nil
	}
	fmt.Printf("# %s\n`%s`\n\n%s\n", note.Title, note.Path, note.Body)
	return nil
}

func runActivity(args []string) error {
	flags := newFlags("activity")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(settings.MetaPath("Activity.md"))
	if err != nil {
		return fmt.Errorf("no timeline yet; run: aimem refresh")
	}
	target := strings.Join(flags.set.Args(), " ")
	if target == "" {
		fmt.Print(string(raw))
		return nil
	}
	heading := fmt.Sprintf("## [[%s]]", target)
	capturing := false
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## ") {
			capturing = strings.EqualFold(line, heading)
			found = found || capturing
			if capturing {
				fmt.Println(line)
			}
			continue
		}
		if capturing {
			fmt.Println(line)
		}
	}
	if !found {
		fmt.Printf("No journal activity recorded for %q.\n", target)
	}
	return nil
}

func runStale(args []string) error {
	flags := newFlags("stale")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	notes, err := notesFor(settings)
	if err != nil {
		return err
	}
	var stale []*vault.Note
	for _, note := range notes {
		if note.IsStale(settings) {
			stale = append(stale, note)
		}
	}
	sort.Slice(stale, func(a, b int) bool { return stale[a].AgeDays > stale[b].AgeDays })

	if *flags.json {
		return emitJSON(stale)
	}
	if len(stale) == 0 {
		fmt.Println("Nothing stale. Every active note has been touched recently.")
		return nil
	}
	fmt.Printf("Active notes untouched for over %d days:\n", settings.StaleDays)
	for _, note := range stale {
		fmt.Printf("  %4dd  %s  (%s)\n", note.AgeDays, note.Title, note.Path)
	}
	return nil
}

func runRemember(args []string) error {
	flags := newFlags("remember")
	target := flags.set.String("note", "", "note to append to (default: this repo's note)")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	fact := strings.Join(flags.set.Args(), " ")
	if fact == "" {
		return fmt.Errorf("usage: aimem remember <durable fact> [--note Title]")
	}

	var note *vault.Note
	if *target != "" {
		notes, err := notesFor(settings)
		if err != nil {
			return err
		}
		if note = search.Resolve(notes, *target); note == nil {
			return fmt.Errorf("no note named %q", *target)
		}
	} else if note, err = repoNote(settings); err != nil {
		return err
	}

	if err := vault.Remember(note, fact); err != nil {
		return err
	}
	fmt.Printf("Remembered in %s\n", note.Path)

	// Only the index is rebuilt, not the repo context blocks. One appended bullet does
	// not change what any repo's CLAUDE.md says, and rewriting forty files to record it
	// made `remember` expensive enough to avoid using.
	result, err := index.Build(settings, indexPath(), false)
	if err != nil {
		return err
	}
	fmt.Printf("Index rebuilt: %d notes\n", result.Notes)
	return nil
}

func runRefresh(args []string) error {
	flags := newFlags("refresh")
	noJournal := flags.set.Bool("no-journal", false, "skip the daily-journal timeline")
	noSync := flags.set.Bool("no-sync", false, "skip writing repo context blocks")
	strict := flags.set.Bool("strict", false, "treat validation warnings as errors")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}

	// Validation gates everything after it. Regenerating from a broken vault propagates
	// the breakage into every agent's context at once, which is worse than not rebuilding.
	report, err := validate.Run(settings)
	if err != nil {
		return err
	}
	printReport(report)
	if !report.OK(*strict) {
		return fmt.Errorf("vault has errors; nothing was regenerated")
	}

	result, err := index.Build(settings, indexPath(), *noJournal)
	if err != nil {
		return err
	}
	if result.Journal != nil {
		fmt.Printf("wrote %s (%d journal files read, %d entries, %d lines held back, %d secrets blocked)\n",
			result.ActivityPath, result.Journal.FilesRead, len(result.Journal.Entries),
			result.Journal.LinesSkipped, result.Journal.SecretsFound)
	}
	fmt.Printf("wrote %s (%d notes, %d bytes)\n", result.BrainPath, result.Notes, result.Bytes)
	if result.JSONPath != "" {
		fmt.Printf("wrote %s\n", result.JSONPath)
	}

	if *noSync || !settings.SyncEnabled {
		return nil
	}
	notes, err := notesFor(settings)
	if err != nil {
		return err
	}
	outcome, err := sync.Run(settings, notes, false)
	if err != nil {
		return err
	}
	for _, skipped := range outcome.Skipped {
		fmt.Printf("skipped %s\n", skipped)
	}
	fmt.Printf("synced %d repo context blocks, %d skipped\n", len(outcome.Written), len(outcome.Skipped))
	return nil
}

func runValidate(args []string) error {
	flags := newFlags("validate")
	strict := flags.set.Bool("strict", false, "treat warnings as errors")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	report, err := validate.Run(settings)
	if err != nil {
		return err
	}
	if *flags.json {
		if err := emitJSON(report); err != nil {
			return err
		}
	} else {
		printReport(report)
	}
	if !report.OK(*strict) {
		os.Exit(1)
	}
	return nil
}

func printReport(report *validate.Report) {
	for _, item := range report.Problems {
		location := item.Path
		if item.Line > 0 {
			location = fmt.Sprintf("%s:%d", item.Path, item.Line)
		}
		marker := "WARN "
		if item.Level == "error" {
			marker = "ERROR"
		}
		fmt.Printf("%s %-22s %s\n        %s\n", marker, item.Check, location, item.Msg)
	}
	fmt.Printf("\n%d notes checked, %d errors, %d warnings\n", report.Notes, report.Errors, report.Warnings)
}

func runInit(args []string) error {
	flags := newFlags("init")
	force := flags.set.Bool("force", false, "overwrite an existing config")
	settings, err := flags.parse(args)
	root := *flags.vault

	var missing *config.ErrNotConfigured
	switch {
	case err == nil && !*force:
		return fmt.Errorf("%s already exists in %s; pass --force to overwrite",
			config.FileName, settings.Root)
	case err != nil && !errors.As(err, &missing) && !*force:
		return err
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	fresh := config.Default(root)

	// Adopt whatever folder layout already exists rather than imposing one. A vault with
	// numbered folders, or Obsidian's defaults, or anything else, keeps its names.
	if adopted := detectFolders(root, fresh); adopted != nil {
		fresh = adopted
	}
	if err := fresh.Validate(); err != nil {
		return err
	}
	if err := fresh.Write(); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", filepath.Join(root, config.FileName))

	for _, folder := range append(append([]string{}, fresh.Shared...), fresh.Meta) {
		path := filepath.Join(root, folder)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			fmt.Printf("created %s/\n", folder)
		}
	}

	// The locked folder holds plaintext while mounted. Gitignoring it at init time means
	// the vault is never briefly in a state where an auto-commit could push that plaintext.
	if fresh.Locked != "" {
		if err := ensureGitignored(root, fresh.Locked); err != nil {
			return err
		}
	}

	if err := config.SavePointer(root); err != nil {
		return err
	}
	fmt.Printf("saved vault pointer to %s\n", config.PointerPath())

	fmt.Printf("\nVault ready at %s\n", root)
	fmt.Printf("Review %s, then run: aimem refresh\n", config.FileName)
	return nil
}

// detectFolders matches existing directories against the default names, ignoring any
// numeric ordering prefix, so `04-Projects` is recognized as the projects folder.
func detectFolders(root string, fallback *config.Config) *config.Config {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	existing := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		existing[strings.ToLower(strings.TrimLeft(entry.Name(), "0123456789-"))] = entry.Name()
	}
	if len(existing) == 0 {
		return nil
	}

	adopt := func(names []string) []string {
		var out []string
		for _, name := range names {
			if actual, ok := existing[strings.ToLower(name)]; ok {
				out = append(out, actual)
			}
		}
		return out
	}
	one := func(name string) string {
		if actual, ok := existing[strings.ToLower(name)]; ok {
			return actual
		}
		return name
	}

	detected := *fallback
	if shared := adopt(fallback.Shared); len(shared) > 0 {
		detected.Shared = shared
	}
	if private := adopt(fallback.Private); len(private) > 0 {
		detected.Private = private
	}
	detected.Locked = one(fallback.Locked)
	detected.Meta = one(fallback.Meta)
	detected.Journal = one(fallback.Journal)
	if !containsString(detected.Private, detected.Journal) {
		detected.Journal = ""
	}
	return &detected
}

// runUse records which vault this machine should use, so every later command can be run
// without a flag. This is the only state aimem keeps outside the vault itself.
func runUse(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: aimem use <path to vault>")
	}
	settings, err := config.Load(args[0])
	if err != nil {
		return err
	}
	if err := config.SavePointer(settings.Root); err != nil {
		return err
	}
	fmt.Printf("Using %s (%s)\n", settings.Name, settings.Root)
	fmt.Printf("Saved to %s\n", config.PointerPath())
	return nil
}

// runConfig prints the resolved contract. --paths exists so shell scripts can ask which
// folders are in a tier instead of restating the list, which is how the MCP allowlist
// drifted from the vault config in the first place.
func runConfig(args []string) error {
	flags := newFlags("config")
	tier := flags.set.String("paths", "", "print absolute folder paths for a tier: shared, private, locked, meta")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}

	if *tier != "" {
		var folders []string
		switch *tier {
		case "shared":
			folders = settings.Shared
		case "private":
			folders = settings.Private
		case "locked":
			folders = []string{settings.Locked}
		case "meta":
			folders = []string{settings.Meta}
		case "readable":
			folders = settings.ReadableFolders()
		default:
			return fmt.Errorf("unknown tier %q; use shared, private, locked, meta, or readable", *tier)
		}
		for _, folder := range folders {
			if folder != "" {
				fmt.Println(filepath.Join(settings.Root, folder))
			}
		}
		return nil
	}

	if *flags.json {
		return emitJSON(settings)
	}
	fmt.Printf("name:     %s\n", settings.Name)
	fmt.Printf("root:     %s\n", settings.Root)
	fmt.Printf("shared:   %s\n", strings.Join(settings.Shared, " "))
	fmt.Printf("private:  %s\n", strings.Join(settings.Private, " "))
	fmt.Printf("locked:   %s\n", settings.Locked)
	fmt.Printf("meta:     %s\n", settings.Meta)
	fmt.Printf("journal:  %s\n", orNone(settings.Journal))
	fmt.Printf("stale:    %d days, applied to %s\n", settings.StaleDays, strings.Join(settings.StaleableTypes, " "))
	fmt.Printf("sync:     %t, owners %s\n", settings.SyncEnabled, orNone(strings.Join(settings.SyncOwners, " ")))
	return nil
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

func runDoctor(args []string) error {
	flags := newFlags("doctor")
	settings, err := flags.parse(args)

	fmt.Printf("aimem %s\n", Version)
	fmt.Printf("vault: %s\n\n", *flags.vault)

	if err != nil {
		fmt.Printf("FAIL  config: %v\n", err)
		return fmt.Errorf("vault is not configured")
	}
	fmt.Printf("ok    config: %s\n", filepath.Join(settings.Root, config.FileName))

	problems := 0
	// Separate wordings: a check that prints its success condition when it fails reads
	// as if the thing it is complaining about is fine.
	check := func(label string, condition bool, whenOK, whenFailed string) {
		if condition {
			fmt.Printf("ok    %s: %s\n", label, whenOK)
			return
		}
		problems++
		fmt.Printf("FAIL  %s: %s\n", label, whenFailed)
	}

	if _, err := os.Stat(filepath.Join(settings.Root, ".git")); err == nil {
		fmt.Println("ok    git: vault is a repository, so `updated` dates are derived")
	} else {
		fmt.Println("warn  git: vault is not a repository; dates fall back to unreliable mtime")
	}

	for _, folder := range settings.Shared {
		path := filepath.Join(settings.Root, folder)
		_, err := os.Stat(path)
		check("shared/"+folder, err == nil, "indexed",
			fmt.Sprintf("listed in %s but missing from disk", config.FileName))
	}
	for _, folder := range settings.Private {
		path := filepath.Join(settings.Root, folder)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("ok    private/%s: present and never walked\n", folder)
		}
	}

	// The boundary check that matters: ask the collector to walk a private folder and
	// confirm it refuses. A privacy guarantee nobody exercises is a comment.
	if len(settings.Private) > 0 {
		_, err := vault.Collect(settings, []string{settings.Private[0]})
		check("boundary", err != nil,
			fmt.Sprintf("collector refuses to walk %q", settings.Private[0]),
			fmt.Sprintf("collector walked %q; the privacy boundary is not being enforced", settings.Private[0]))
	}

	if settings.Locked != "" {
		ignore, _ := os.ReadFile(filepath.Join(settings.Root, ".gitignore"))
		check("locked", strings.Contains(string(ignore), settings.Locked),
			fmt.Sprintf("%s is gitignored, so unlocked plaintext cannot be committed", settings.Locked),
			fmt.Sprintf("%s is NOT gitignored; unlocked plaintext could be committed and pushed. Fix: echo '%s/' >> %s",
				settings.Locked, settings.Locked, filepath.Join(settings.Root, ".gitignore")))
	}

	if _, err := os.Stat(settings.MetaPath("BRAIN.md")); err == nil {
		fmt.Printf("ok    index: %s\n", settings.MetaPath("BRAIN.md"))
	} else {
		problems++
		fmt.Println("FAIL  index: no BRAIN.md; run `aimem refresh`")
	}

	report, err := validate.Run(settings)
	if err != nil {
		return err
	}
	check("schema", report.Errors == 0,
		fmt.Sprintf("%d notes, %d errors, %d warnings", report.Notes, report.Errors, report.Warnings),
		fmt.Sprintf("%d notes, %d errors, %d warnings; run `aimem validate` for detail",
			report.Notes, report.Errors, report.Warnings))

	fmt.Println()
	if problems > 0 {
		return fmt.Errorf("%d checks failed", problems)
	}
	fmt.Println("All checks passed.")
	return nil
}

func runMCP(args []string) error {
	flags := newFlags("mcp")
	writable := flags.set.Bool("write", false, "allow vault_remember to modify notes")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	return mcp.New(settings, *writable).Serve()
}

func runInstallHooks(args []string) error {
	flags := newFlags("install-hooks")
	settings, err := flags.parse(args)
	if err != nil {
		return err
	}
	hooks := filepath.Join(settings.Root, ".git", "hooks")
	if _, err := os.Stat(filepath.Join(settings.Root, ".git")); err != nil {
		return fmt.Errorf("%s is not a git repository", settings.Root)
	}
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return err
	}
	target := filepath.Join(hooks, "pre-commit")
	// Lstat, not Stat: earlier versions installed the hook as a symlink into the aimem
	// checkout, which leaves a dangling link once that file moves. Stat follows the link
	// and reports "missing", and the write then fails on the link itself.
	if info, err := os.Lstat(target); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if err := os.Remove(target); err != nil {
				return err
			}
		default:
			existing, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			if !strings.Contains(string(existing), "aimem") {
				return fmt.Errorf("refusing to replace a hand-written hook at %s", target)
			}
		}
	}

	binary, err := os.Executable()
	if err != nil {
		binary = "aimem"
	}
	hook := fmt.Sprintf(`#!/usr/bin/env sh
# Installed by aimem. Validates the vault and regenerates the index so a commit
# cannot leave generated context drifting behind the notes it describes.
# Bypass with: git commit --no-verify
set -e
AIMEM=%q
command -v "$AIMEM" >/dev/null 2>&1 || AIMEM=aimem
command -v "$AIMEM" >/dev/null 2>&1 || exit 0

"$AIMEM" validate --vault "$(git rev-parse --show-toplevel)" || {
  echo "Vault has schema errors. Fix them, or commit with --no-verify." >&2
  exit 1
}
"$AIMEM" refresh --vault "$(git rev-parse --show-toplevel)" --no-sync >/dev/null
git add -- "%s/BRAIN.md" "%s/Activity.md" 2>/dev/null || true
exit 0
`, binary, settings.Meta, settings.Meta)

	if err := os.WriteFile(target, []byte(hook), 0o755); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", target)
	return nil
}

// ensureGitignored adds a folder to the vault's .gitignore if it is not already covered.
func ensureGitignored(root, folder string) error {
	path := filepath.Join(root, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "/")) == folder {
			return nil
		}
	}
	body := strings.TrimRight(string(existing), "\n")
	if body != "" {
		body += "\n"
	}
	body += fmt.Sprintf("# Plaintext exists here only while unlocked; never commit it.\n%s/\n", folder)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Printf("gitignored %s/\n", folder)
	return nil
}

func emitJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
