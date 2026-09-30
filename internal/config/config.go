// Package config holds everything aimem needs to know about a specific vault.
//
// This package exists so that nothing personal is compiled into aimem. Folder names,
// note types, statuses, and thresholds all come from a single `.aimem.yml` at the vault
// root, which means the vault is self-describing: clone it anywhere and aimem knows how
// to read it, with no second file to keep in sync and nothing to recreate per machine.
//
// The folder tiers in here are the privacy boundary, so Validate is strict about them.
// A config that puts one folder in two tiers, or that escapes the vault root with `..`,
// is refused rather than interpreted.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileName is the single per-vault config file, kept at the vault root so it travels
// with the notes it describes.
const FileName = ".aimem.yml"

// Config is the parsed vault contract.
type Config struct {
	Root string `json:"root"` // absolute path; resolved, never read from the file
	Name string `json:"name"`

	// Folder tiers. Shared is indexed and searchable, Private is never opened except by
	// the audited journal bridge, Locked is never opened at all, and Meta is validated
	// but excluded from the index because it holds the generated output itself.
	Shared  []string `json:"shared"`
	Private []string `json:"private"`
	Locked  string   `json:"locked"`
	Meta    string   `json:"meta"`

	// Journal is the one private folder the activity bridge may read. It must be listed
	// in Private: the bridge narrows private access, it does not create it.
	Journal string `json:"journal"`

	Types          []string `json:"types"`
	Statuses       []string `json:"statuses"`
	StaleableTypes []string `json:"staleable_types"`
	StaleDays      int      `json:"stale_days"`

	JournalEntriesPerProject int `json:"journal_entries_per_project"`

	SyncEnabled bool     `json:"sync_enabled"`
	SyncOwners  []string `json:"sync_owners"`
	SyncSkip    []string `json:"sync_skip"`
}

// Default returns a generic vault contract. These names are deliberately plain: aimem
// ships knowing nothing about anyone's vault, and `aimem init` writes this out as a
// starting point the user then edits.
func Default(root string) *Config {
	return &Config{
		Root:                     root,
		Name:                     filepath.Base(root),
		Shared:                   []string{"Companies", "Projects", "Research", "Ideas", "Reference"},
		Private:                  []string{"Inbox", "Daily", "Personal"},
		Locked:                   "Locked",
		Meta:                     "Meta",
		Journal:                  "Daily",
		Types:                    []string{"project", "company", "research", "idea", "reference", "journal", "dashboard"},
		Statuses:                 []string{"active", "paused", "shipped", "dead", "evergreen"},
		StaleableTypes:           []string{"project", "company"},
		StaleDays:                30,
		JournalEntriesPerProject: 12,
		SyncEnabled:              true,
	}
}

// ErrNotConfigured is returned when a vault has no config file. Callers turn this into
// an instruction to run `aimem init` rather than silently indexing nothing, which is the
// failure mode where an empty index looks like an empty vault.
type ErrNotConfigured struct{ Root string }

func (e *ErrNotConfigured) Error() string {
	return fmt.Sprintf("no %s in %s\n\nThis vault is not set up yet. Run:\n    aimem init --vault %s",
		FileName, e.Root, e.Root)
}

// PointerPath is a one-line file holding the path to this machine's vault.
//
// It exists because the vault's location is the one thing that cannot live inside the
// vault. Everything else aimem knows is in .aimem.yml and travels with the notes; this
// holds a single path and nothing else, so there is still only one file to edit.
// An explicit --vault flag or AIMEM_VAULT env var overrides it.
func PointerPath() string {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "aimem", "vault")
}

// SavePointer records the vault path for later commands.
func SavePointer(root string) error {
	path := PointerPath()
	if path == "" {
		return fmt.Errorf("cannot determine a config directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(expand(root)+"\n"), 0o644)
}

// DefaultRoot resolves the vault location, most explicit first: AIMEM_VAULT, then the
// legacy OBBY_VAULT, then the saved pointer, then a conventional path.
func DefaultRoot() string {
	for _, name := range []string{"AIMEM_VAULT", "OBBY_VAULT"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return expand(value)
		}
	}
	if path := PointerPath(); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if saved := strings.TrimSpace(string(raw)); saved != "" {
				return expand(saved)
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "vault"
	}
	return filepath.Join(home, "vault")
}

func expand(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}

// Load reads and validates the config at the given vault root.
func Load(root string) (*Config, error) {
	root = expand(root)
	raw, err := os.ReadFile(filepath.Join(root, FileName))
	if os.IsNotExist(err) {
		return nil, &ErrNotConfigured{Root: root}
	}
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(raw), "\t") {
		return nil, fmt.Errorf("%s: tabs are not valid YAML indentation; use spaces", FileName)
	}

	tree := parseYAML(string(raw))
	defaults := Default(root)
	folders := tree.child("folders")
	schema := tree.child("schema")
	sync := tree.child("sync")

	// The folder tiers are the privacy boundary, so they are required rather than
	// defaulted. Silently substituting built-in names for a section that failed to parse
	// would mean the user's actual private folders are in no tier at all, which reads as
	// "not private" everywhere downstream. Failing to load is the safe direction.
	for _, required := range []string{"shared", "private", "meta"} {
		if !folders.has(required) {
			return nil, fmt.Errorf("%s: folders.%s is required; it is part of the privacy boundary and is not defaulted",
				FileName, required)
		}
	}
	if len(folders.list("shared")) == 0 {
		return nil, fmt.Errorf("%s: folders.shared parsed as empty", FileName)
	}

	loaded := &Config{
		Root:                     root,
		Name:                     firstNonEmpty(tree.text("name"), defaults.Name),
		Shared:                   folders.list("shared"),
		Private:                  folders.list("private"),
		Locked:                   folders.text("locked"),
		Meta:                     folders.text("meta"),
		Journal:                  defaults.Journal,
		Types:                    firstNonEmptyList(schema.list("types"), defaults.Types),
		Statuses:                 firstNonEmptyList(schema.list("statuses"), defaults.Statuses),
		StaleableTypes:           firstNonEmptyList(schema.list("staleable_types"), defaults.StaleableTypes),
		StaleDays:                schema.number("stale_days", defaults.StaleDays),
		JournalEntriesPerProject: schema.number("journal_entries_per_project", defaults.JournalEntriesPerProject),
		SyncEnabled:              sync.boolean("enabled", defaults.SyncEnabled),
		SyncOwners:               sync.list("owners"),
		SyncSkip:                 sync.list("skip"),
	}
	// An explicitly empty `journal:` disables the bridge, so presence is checked rather
	// than emptiness. Every other field falls back to the default when blank.
	if folders.has("journal") {
		loaded.Journal = folders.text("journal")
	}

	if err := loaded.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(root, FileName), err)
	}
	return loaded, nil
}

// Validate refuses a config that would weaken the privacy boundary. Every rule here
// exists because breaking it would silently expose private notes rather than fail.
func (c *Config) Validate() error {
	if c.Root == "" {
		return fmt.Errorf("vault root is empty")
	}

	tiers := []struct {
		label   string
		folders []string
	}{
		{"folders.shared", c.Shared},
		{"folders.private", c.Private},
		{"folders.locked", []string{c.Locked}},
		{"folders.meta", []string{c.Meta}},
	}

	// A folder name must be one clean path segment. Without this a config could name
	// "../.." as shared and walk the entire home directory.
	seen := map[string]string{}
	for _, tier := range tiers {
		for _, folder := range tier.folders {
			if folder == "" {
				continue
			}
			if folder != filepath.Clean(folder) || strings.ContainsAny(folder, `/\`) || folder == ".." || folder == "." {
				return fmt.Errorf("%s: %q must be a single folder name inside the vault", tier.label, folder)
			}
			if where, duplicate := seen[folder]; duplicate {
				return fmt.Errorf("%q is listed in both %s and %s; a folder belongs to exactly one tier",
					folder, where, tier.label)
			}
			seen[folder] = tier.label
		}
	}

	if c.Meta == "" {
		return fmt.Errorf("folders.meta is required; it is where BRAIN.md and Activity.md are written")
	}
	if len(c.Shared) == 0 {
		return fmt.Errorf("folders.shared is empty; there would be nothing to index")
	}
	// The journal bridge reads one private folder. Pointing it at a shared folder would
	// be harmless but pointless; pointing it outside the private tier would mean the
	// bridge is granting access rather than narrowing it.
	if c.Journal != "" && !contains(c.Private, c.Journal) {
		return fmt.Errorf("folders.journal %q must be listed in folders.private", c.Journal)
	}
	if len(c.Types) == 0 {
		return fmt.Errorf("schema.types is empty")
	}
	if len(c.Statuses) == 0 {
		return fmt.Errorf("schema.statuses is empty")
	}
	for _, kind := range c.StaleableTypes {
		if !contains(c.Types, kind) {
			return fmt.Errorf("schema.staleable_types names %q, which is not in schema.types", kind)
		}
	}
	if c.StaleDays < 1 {
		return fmt.Errorf("schema.stale_days must be at least 1")
	}
	return nil
}

// IsPrivate reports whether a vault-relative path falls in the private or locked tier.
// Everything that walks the vault asks this rather than reimplementing the check.
func (c *Config) IsPrivate(relative string) bool {
	top := topFolder(relative)
	if top == "" {
		return false
	}
	return contains(c.Private, top) || (c.Locked != "" && top == c.Locked)
}

// IsShared reports whether a vault-relative path is in the agent-readable tier. Meta is
// shared for reading (it holds the generated index) but is excluded from the index.
func (c *Config) IsShared(relative string) bool {
	top := topFolder(relative)
	return top != "" && (contains(c.Shared, top) || top == c.Meta)
}

// IndexFolders are the folders whose notes become index entries.
func (c *Config) IndexFolders() []string { return append([]string{}, c.Shared...) }

// ValidateFolders are the folders the validator checks: everything readable, including
// Meta, because a broken generated note is still a broken note.
func (c *Config) ValidateFolders() []string { return append(append([]string{}, c.Shared...), c.Meta) }

// ReadableFolders are the folders an agent may read through, in tier order.
func (c *Config) ReadableFolders() []string { return c.ValidateFolders() }

func (c *Config) MetaPath(name string) string { return filepath.Join(c.Root, c.Meta, name) }

func topFolder(relative string) string {
	relative = filepath.ToSlash(filepath.Clean(relative))
	if relative == "." || relative == "" || strings.HasPrefix(relative, "..") {
		return ""
	}
	if cut := strings.Index(relative, "/"); cut >= 0 {
		return relative[:cut]
	}
	return relative
}

// Write renders the config to the vault root, with comments explaining each field. The
// file is meant to be edited by hand, so it is generated to be read, not round-tripped.
func (c *Config) Write() error {
	var out strings.Builder
	out.WriteString("# aimem vault configuration.\n")
	out.WriteString("# This file is the only place aimem learns anything about this vault.\n")
	out.WriteString("# It lives at the vault root so it travels with the notes it describes.\n\n")
	out.WriteString(fmt.Sprintf("name: %s\n\n", c.Name))

	out.WriteString("# Folder tiers. This is the privacy boundary, not a preference.\n")
	out.WriteString("#   shared  indexed, searchable, exposed to agents\n")
	out.WriteString("#   private never opened, except the journal bridge below\n")
	out.WriteString("#   locked  never opened at all, mounted or not\n")
	out.WriteString("#   meta    holds the generated index; validated but not indexed\n")
	out.WriteString("folders:\n")
	writeList(&out, "shared", c.Shared)
	writeList(&out, "private", c.Private)
	out.WriteString(fmt.Sprintf("  locked: %s\n", c.Locked))
	out.WriteString(fmt.Sprintf("  meta: %s\n", c.Meta))
	out.WriteString("  # The one private folder the activity bridge may read. Only lines linking\n")
	out.WriteString("  # to an already-shared note leave it. Set to \"\" to disable the bridge.\n")
	out.WriteString(fmt.Sprintf("  journal: %s\n\n", c.Journal))

	out.WriteString("schema:\n")
	writeList(&out, "types", c.Types)
	writeList(&out, "statuses", c.Statuses)
	out.WriteString("  # Types where `active` is a claim silence can falsify. Ideas and research\n")
	out.WriteString("  # accrete rather than rot, so warning about them trains you to ignore warnings.\n")
	writeList(&out, "staleable_types", c.StaleableTypes)
	out.WriteString(fmt.Sprintf("  stale_days: %d\n", c.StaleDays))
	out.WriteString(fmt.Sprintf("  journal_entries_per_project: %d\n\n", c.JournalEntriesPerProject))

	out.WriteString("# Writing the generated context block into each mapped repo's CLAUDE.md.\n")
	out.WriteString("sync:\n")
	out.WriteString(fmt.Sprintf("  enabled: %t\n", c.SyncEnabled))
	out.WriteString("  # Only repos whose git remote matches one of these owners are written to.\n")
	out.WriteString("  # Empty means every mapped repo. This is what keeps aimem out of other\n")
	out.WriteString("  # people's checkouts without hardcoding their paths.\n")
	writeList(&out, "owners", c.SyncOwners)
	writeList(&out, "skip", c.SyncSkip)

	return os.WriteFile(filepath.Join(c.Root, FileName), []byte(out.String()), 0o644)
}

func writeList(out *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		out.WriteString(fmt.Sprintf("  %s: []\n", key))
		return
	}
	out.WriteString(fmt.Sprintf("  %s:\n", key))
	for _, value := range values {
		out.WriteString(fmt.Sprintf("    - %s\n", value))
	}
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func firstNonEmptyList(value, fallback []string) []string {
	if len(value) > 0 {
		return value
	}
	return fallback
}

// Sorted returns a copy of a list in sorted order, for stable output.
func Sorted(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
