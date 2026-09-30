package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSubset(t *testing.T) {
	tree := parseYAML(`
name: Demo   # trailing comment

folders:
  shared:
    - Projects
    - "Research"
  private: [Inbox, Daily]
  meta: Meta

schema:
  stale_days: 45
  types: [project, idea]

sync:
  enabled: false
`)
	if got := tree.text("name"); got != "Demo" {
		t.Errorf("name = %q, want Demo", got)
	}
	folders := tree.child("folders")
	if got := folders.list("shared"); len(got) != 2 || got[0] != "Projects" || got[1] != "Research" {
		t.Errorf("shared = %#v", got)
	}
	if got := folders.list("private"); len(got) != 2 || got[1] != "Daily" {
		t.Errorf("private = %#v", got)
	}
	if got := tree.child("schema").number("stale_days", 30); got != 45 {
		t.Errorf("stale_days = %d, want 45", got)
	}
	if tree.child("sync").boolean("enabled", true) {
		t.Error("enabled = true, want false")
	}
	if got := tree.child("schema").number("missing", 7); got != 7 {
		t.Errorf("missing key = %d, want the fallback 7", got)
	}
}

func TestStripCommentKeepsQuotedHash(t *testing.T) {
	if got := stripComment(`name: "a # b"  # real comment`); !strings.Contains(got, "a # b") {
		t.Errorf("stripComment removed a quoted hash: %q", got)
	}
}

// TestRoundTrip is what makes `aimem init` trustworthy: the file it writes must parse
// back to the same contract, or a fresh vault is misconfigured from the first command.
func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	original := Default(root)
	original.Name = "Roundtrip"
	original.StaleDays = 45
	original.SyncOwners = []string{"someone"}
	if err := original.Write(); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "Roundtrip" || loaded.StaleDays != 45 {
		t.Errorf("round trip lost fields: %+v", loaded)
	}
	if len(loaded.Shared) != len(original.Shared) {
		t.Errorf("shared = %#v, want %#v", loaded.Shared, original.Shared)
	}
	if len(loaded.SyncOwners) != 1 || loaded.SyncOwners[0] != "someone" {
		t.Errorf("owners = %#v", loaded.SyncOwners)
	}
}

// TestValidateRejectsUnsafeConfigs covers every way a config could weaken the privacy
// boundary. These are the cases where being permissive means leaking rather than erroring.
func TestValidateRejectsUnsafeConfigs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"folder in two tiers", func(c *Config) { c.Shared = append(c.Shared, c.Private[0]) }},
		{"escapes the vault root", func(c *Config) { c.Shared = append(c.Shared, "..") }},
		{"nested path as folder", func(c *Config) { c.Shared = append(c.Shared, "a/b") }},
		{"parent traversal in a name", func(c *Config) { c.Shared = append(c.Shared, "../../home") }},
		{"journal outside the private tier", func(c *Config) { c.Journal = c.Shared[0] }},
		{"locked collides with shared", func(c *Config) { c.Locked = c.Shared[0] }},
		{"meta collides with private", func(c *Config) { c.Meta = c.Private[0] }},
		{"no shared folders", func(c *Config) { c.Shared = nil }},
		{"no meta folder", func(c *Config) { c.Meta = "" }},
		{"staleable type not a real type", func(c *Config) { c.StaleableTypes = []string{"nonsense"} }},
		{"zero stale threshold", func(c *Config) { c.StaleDays = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := Default("/tmp/vault")
			test.mutate(settings)
			if err := settings.Validate(); err == nil {
				t.Errorf("Validate accepted an unsafe config: %s", test.name)
			}
		})
	}

	if err := Default("/tmp/vault").Validate(); err != nil {
		t.Errorf("Validate rejected the default config: %v", err)
	}
}

func TestEmptyJournalDisablesBridge(t *testing.T) {
	root := t.TempDir()
	settings := Default(root)
	settings.Journal = ""
	if err := settings.Write(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Journal != "" {
		t.Errorf("Journal = %q; an explicitly empty journal must stay disabled, not fall back", loaded.Journal)
	}
}

func TestMissingConfigIsActionable(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("Load accepted a vault with no config")
	}
	if !strings.Contains(err.Error(), "aimem init") {
		t.Errorf("error does not tell the user what to do: %v", err)
	}
}

func TestTierMembership(t *testing.T) {
	settings := Default("/tmp/vault")
	for _, path := range []string{"Projects", "Projects/Alpha.md", "Meta/BRAIN.md"} {
		if !settings.IsShared(path) {
			t.Errorf("IsShared(%q) = false", path)
		}
		if settings.IsPrivate(path) {
			t.Errorf("IsPrivate(%q) = true", path)
		}
	}
	for _, path := range []string{"Daily", "Daily/2026-01-01.md", "Locked/Keys.md", "Personal"} {
		if !settings.IsPrivate(path) {
			t.Errorf("IsPrivate(%q) = false", path)
		}
		if settings.IsShared(path) {
			t.Errorf("IsShared(%q) = true", path)
		}
	}
	// A traversal attempt belongs to no tier, so it is neither readable nor mistakenly
	// treated as private-and-therefore-handled.
	for _, path := range []string{"../escape", "..", ""} {
		if settings.IsShared(path) {
			t.Errorf("IsShared(%q) = true", path)
		}
	}
}

func TestTabsAreRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("folders:\n\tmeta: Meta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "tabs") {
		t.Errorf("expected a tab-specific error, got %v", err)
	}
}

// TestFolderTiersAreRequired guards against the worst silent failure available here: a
// folders section that fails to parse falling back to built-in names, which would leave
// the user's real private folders in no tier and therefore unprotected.
func TestFolderTiersAreRequired(t *testing.T) {
	for _, body := range []string{
		"name: X\n",
		"name: X\nfolders:\n  shared: [Projects]\n  meta: Meta\n",
		"name: X\nfolders:\n  private: [Daily]\n  meta: Meta\n",
		"name: X\nfolders:\n  shared: []\n  private: [Daily]\n  meta: Meta\n",
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, FileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Errorf("Load accepted a config with incomplete folder tiers:\n%s", body)
		}
	}
}

func TestJournalFilenameMustVaryWithTheDate(t *testing.T) {
	for _, layout := range []string{"daily", "", "notes/2006-01-02"} {
		settings := Default("/tmp/vault")
		settings.JournalFilename = layout
		if err := settings.Validate(); err == nil {
			t.Errorf("Validate accepted journal_filename %q", layout)
		}
	}
	for _, layout := range []string{"2006-01-02", "2006_01_02", "20060102", "Jan-2-2006"} {
		settings := Default("/tmp/vault")
		settings.JournalFilename = layout
		if err := settings.Validate(); err != nil {
			t.Errorf("Validate rejected a usable layout %q: %v", layout, err)
		}
	}
	// With no journal configured the layout is irrelevant and must not block loading.
	settings := Default("/tmp/vault")
	settings.Journal, settings.JournalFilename = "", ""
	if err := settings.Validate(); err != nil {
		t.Errorf("Validate rejected a vault with no journal: %v", err)
	}
}

// TestRepoPathsArePortable covers the property that makes one vault usable on several
// machines: a note says where a checkout lives in terms each machine can resolve for
// itself, instead of hardcoding one person's home directory.
func TestRepoPathsArePortable(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	settings := Default("/tmp/vault")
	settings.CodeRoot = filepath.Join(home, "Projects")

	tests := []struct {
		raw  string
		want string
	}{
		{"company/turtosa", filepath.Join(home, "Projects", "company", "turtosa")},
		{"~/Code/thing", filepath.Join(home, "Code", "thing")},
		{"/opt/elsewhere", "/opt/elsewhere"},
		{"", ""},
	}
	for _, test := range tests {
		if got := settings.ResolveRepo(test.raw); got != test.want {
			t.Errorf("ResolveRepo(%q) = %q, want %q", test.raw, got, test.want)
		}
	}

	// And the migration direction: an absolute path becomes the shortest form that
	// resolves back to the same directory. Round-tripping is the safety property —
	// a rewrite that pointed at a different checkout would be worse than no rewrite.
	for _, absolute := range []string{
		filepath.Join(home, "Projects", "company", "turtosa"),
		filepath.Join(home, "Code", "thing"),
		"/opt/elsewhere",
	} {
		portable := settings.PortableRepo(absolute)
		if got := settings.ResolveRepo(portable); got != absolute {
			t.Errorf("PortableRepo(%q) = %q, which resolves to %q", absolute, portable, got)
		}
	}

	// A path under CodeRoot must produce the shortest form, not merely a working one.
	inside := filepath.Join(home, "Projects", "aimem")
	if got := settings.PortableRepo(inside); got != "aimem" {
		t.Errorf("PortableRepo(%q) = %q, want the code-root-relative form", inside, got)
	}
}
