// Package boundary tests the privacy boundary end to end.
//
// Every other test in this repo checks that the right content comes out. These check
// that the wrong content does not, across every surface that can emit text: the
// collector, the generated index, the journal timeline, the JSON index, the validator's
// own report, ranked search, and every MCP tool response.
//
// The two claims are not the same. "The index contains Alpha" can pass while private
// notes leak; only "no output anywhere contains the canary" is the security property, and
// it is the one a stranger has to trust before adopting this.
package boundary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/index"
	"github.com/joeykokinda/aimem/internal/mcp"
	"github.com/joeykokinda/aimem/internal/search"
	"github.com/joeykokinda/aimem/internal/testvault"
	"github.com/joeykokinda/aimem/internal/validate"
	"github.com/joeykokinda/aimem/internal/vault"
)

// TestCollectorRefusesPrivateFolders asserts the collector fails loudly rather than
// quietly returning nothing. A silent empty result would let a caller bug look like an
// empty folder.
func TestCollectorRefusesPrivateFolders(t *testing.T) {
	settings := testvault.Build(t)
	for _, folder := range append(append([]string{}, settings.Private...), settings.Locked) {
		if _, err := vault.Collect(settings, []string{folder}); err == nil {
			t.Errorf("Collect(%q) succeeded; private folders must be refused", folder)
		}
	}
}

// TestSharedCollectionExcludesPrivate covers both the folder tier and the per-note
// `scope: private` opt-out, which lives inside a shared folder.
func TestSharedCollectionExcludesPrivate(t *testing.T) {
	settings := testvault.Build(t)
	notes, err := vault.Collect(settings, settings.IndexFolders())
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range notes {
		testvault.AssertNoCanary(t, "note "+note.Path, note.Body)
		if note.Title == "Secretive" {
			t.Error("a note marked `scope: private` was collected")
		}
	}
	if len(notes) == 0 {
		t.Fatal("collected nothing; the test vault is not being read at all")
	}
}

// TestGeneratedArtifactsAreClean walks every byte aimem writes into the vault or the
// state directory.
func TestGeneratedArtifactsAreClean(t *testing.T) {
	settings := testvault.Build(t)
	jsonPath := filepath.Join(t.TempDir(), "brain.json")

	result, err := index.Build(settings, jsonPath, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{result.BrainPath, result.ActivityPath, result.JSONPath} {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		testvault.AssertNoCanary(t, filepath.Base(path), string(raw))
	}

	// Sanity: the artifacts must actually contain the shared vault, or "no canary" is
	// trivially true because nothing was generated.
	brain, _ := os.ReadFile(result.BrainPath)
	if !strings.Contains(string(brain), "Alpha") {
		t.Error("BRAIN.md does not mention the shared project; generation is broken")
	}
	if strings.Contains(string(brain), "Secretive") {
		t.Error("BRAIN.md names a `scope: private` note")
	}
}

// TestJournalBridgeContract exercises each rule the activity bridge promises: only lines
// linking to a shared note escape, #private opts out, %%comments%% are stripped, and
// secrets are blocked.
func TestJournalBridgeContract(t *testing.T) {
	settings := testvault.Build(t)
	result, err := index.Build(settings, "", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(result.ActivityPath)
	if err != nil {
		t.Fatal(err)
	}
	timeline := string(raw)

	testvault.AssertNoCanary(t, "Activity.md", timeline)
	if !strings.Contains(timeline, "the indexer is faster now") {
		t.Error("a journal line linking to a shared note did not reach the timeline")
	}
	if strings.Contains(timeline, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("a journal line containing a secret reached the timeline")
	}
	if result.Journal.SecretsFound == 0 {
		t.Error("the secret scanner did not fire on a line that contains an AWS key")
	}
}

// TestJournalOutsidePrivateTierIsRefused covers the misconfiguration where the bridge is
// pointed at a folder it has no business reading.
func TestJournalOutsidePrivateTierIsRefused(t *testing.T) {
	settings := testvault.Build(t)
	settings.Journal = "Projects" // shared, therefore not a bridge
	if _, err := index.Build(settings, "", false); err == nil {
		t.Error("a journal outside the private tier was accepted")
	}
}

// TestValidatorReportIsClean matters because the validator reads private note *names* to
// resolve wikilinks, so it is the one component with a legitimate reason to touch the
// private tier at all.
func TestValidatorReportIsClean(t *testing.T) {
	settings := testvault.Build(t)
	report, err := validate.Run(settings)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	testvault.AssertNoCanary(t, "validator report", string(encoded))
	if report.Notes == 0 {
		t.Fatal("validator checked nothing")
	}
}

// TestSearchCannotReachPrivateContent searches for the canary directly. This is the
// adversarial case: an agent that has somehow learned a private string and tries to pull
// the surrounding note through the search tool.
func TestSearchCannotReachPrivateContent(t *testing.T) {
	settings := testvault.Build(t)
	notes, err := vault.Collect(settings, settings.IndexFolders())
	if err != nil {
		t.Fatal(err)
	}
	hits := search.Run(notes, search.Query{Terms: []string{testvault.Canary}, Limit: 50, Context: 5})
	if len(hits) != 0 {
		t.Errorf("search for private content returned %d hits", len(hits))
	}
	if found := search.Run(notes, search.Query{Terms: []string{"alpha"}, Limit: 5}); len(found) == 0 {
		t.Error("search found nothing for a shared term; the test is not exercising search")
	}
}

// TestMCPToolsAreClean drives the real JSON-RPC surface, because that is what an agent
// actually talks to. Every tool is called, including with hostile arguments asking
// directly for private paths.
func TestMCPToolsAreClean(t *testing.T) {
	settings := testvault.Build(t)
	if _, err := index.Build(settings, "", false); err != nil {
		t.Fatal(err)
	}

	calls := []struct {
		tool      string
		arguments map[string]any
	}{
		{"vault_context", nil},
		{"vault_context", map[string]any{"project": "Alpha"}},
		{"vault_context", map[string]any{"project": "Secretive"}},
		{"vault_search", map[string]any{"query": testvault.Canary}},
		{"vault_search", map[string]any{"query": "alpha"}},
		{"vault_search", map[string]any{"query": "", "status": "active"}},
		{"vault_note", map[string]any{"name": "Alpha"}},
		{"vault_note", map[string]any{"name": "Secretive"}},
		{"vault_note", map[string]any{"name": "Health"}},
		{"vault_note", map[string]any{"name": "Keys"}},
		{"vault_note", map[string]any{"name": "Inbox/Scratch.md"}},
		{"vault_note", map[string]any{"name": "../../../etc/passwd"}},
		{"vault_repo", map[string]any{"path": "/nonexistent/alpha"}},
	}

	transcript := exchange(t, settings, calls)
	testvault.AssertNoCanary(t, "MCP transcript", transcript)
	if strings.Contains(transcript, "root:") {
		t.Error("MCP transcript contains what looks like /etc/passwd content")
	}
	if !strings.Contains(transcript, "Alpha") {
		t.Error("MCP returned nothing about the shared project; the test is not exercising the server")
	}
}

// TestMCPIsReadOnlyByDefault asserts the write tool is neither listed nor callable
// unless the server was started with writes enabled.
func TestMCPIsReadOnlyByDefault(t *testing.T) {
	settings := testvault.Build(t)

	listing := exchangeRaw(t, settings, false, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(listing, "vault_remember") {
		t.Error("a read-only server advertises the write tool")
	}
	for _, name := range []string{"write_file", "edit_file", "move_file", "create_directory"} {
		if strings.Contains(listing, name) {
			t.Errorf("server exposes filesystem mutation tool %q", name)
		}
	}

	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` +
		`{"name":"vault_remember","arguments":{"note":"Alpha","fact":"should not land"}}}`
	refusal := exchangeRaw(t, settings, false, call)
	if !strings.Contains(refusal, "read-only") {
		t.Errorf("read-only server did not refuse a write: %s", refusal)
	}

	body, err := os.ReadFile(filepath.Join(settings.Root, "Projects", "Alpha.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "should not land") {
		t.Error("a read-only server modified a note")
	}
}

// TestMCPWriteRefusesSecrets covers the one path where agent-composed text enters a
// vault that gets pushed.
func TestMCPWriteRefusesSecrets(t *testing.T) {
	settings := testvault.Build(t)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
		`{"name":"vault_remember","arguments":{"note":"Alpha","fact":"token is ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`
	response := exchangeRaw(t, settings, true, call)
	if !strings.Contains(response, "refusing to write") {
		t.Errorf("a secret was accepted into the vault: %s", response)
	}
}

// exchange runs a full initialize-then-call session and returns the whole transcript.
func exchange(t *testing.T, settings *config.Config, calls []struct {
	tool      string
	arguments map[string]any
}) string {
	t.Helper()
	var requests []string
	requests = append(requests, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	requests = append(requests, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for index, call := range calls {
		arguments, err := json.Marshal(call.arguments)
		if err != nil {
			t.Fatal(err)
		}
		if call.arguments == nil {
			arguments = []byte("{}")
		}
		requests = append(requests, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
			index+2, call.tool, arguments))
	}
	return exchangeRaw(t, settings, false, requests...)
}

func exchangeRaw(t *testing.T, settings *config.Config, writable bool, requests ...string) string {
	t.Helper()
	server := mcp.New(settings, writable)
	input := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var output strings.Builder
	mcp.SetTransport(server, input, &output)
	if err := server.Serve(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return output.String()
}
