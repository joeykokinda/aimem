package boundary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joeykokinda/aimem/internal/index"
	"github.com/joeykokinda/aimem/internal/testvault"
)

// TestRepoLookupIgnoresNotesWithoutRepos pins a bug that made `aimem project` and
// vault_repo return an arbitrary note: a note with no `repo:` field turned the prefix
// test into HasPrefix(path, "/"), which every absolute path satisfies, so whichever
// repo-less note came first in path order won.
func TestRepoLookupIgnoresNotesWithoutRepos(t *testing.T) {
	settings := testvault.Build(t)
	// Beta, Acme and Indexing have no repo; only Alpha claims /nonexistent/alpha.
	if _, err := index.Build(settings, "", true); err != nil {
		t.Fatal(err)
	}

	claimed, err := json.Marshal(testvault.AlphaRepo)
	if err != nil {
		t.Fatal(err)
	}
	answer := exchangeRaw(t, settings, false,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"vault_repo","arguments":{"path":`+string(claimed)+`}}}`)
	if !strings.Contains(answer, "Alpha") {
		t.Errorf("vault_repo did not resolve the claiming note: %s", answer)
	}

	unclaimed := exchangeRaw(t, settings, false,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"vault_repo","arguments":{"path":"/some/unrelated/checkout"}}}`)
	if !strings.Contains(unclaimed, "no vault note claims") {
		t.Errorf("an unclaimed path matched a repo-less note: %s", unclaimed)
	}
}
