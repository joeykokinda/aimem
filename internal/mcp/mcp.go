// Package mcp serves the vault to agents over the Model Context Protocol.
//
// # WHY THIS EXISTS
//
// The obvious way to give an agent a Markdown vault is to point a generic filesystem MCP
// server at the shared folders. That works, and it quietly undoes most of the guarantees
// the rest of this tool makes:
//
//   - `scope: private` in a note's frontmatter is honored by the indexer, so the note
//     vanishes from BRAIN.md while remaining perfectly readable over the filesystem.
//     A boundary that only hides things from the index is not a boundary.
//   - The folder allowlist gets restated in the MCP client's config, by hand, where it
//     drifts from the vault config the moment either one changes.
//   - A filesystem server exposes write, move, and delete alongside read, so "do not
//     edit the vault unprompted" becomes a policy in a prompt rather than a property of
//     the system.
//
// This server closes all three. It reads the same config every other command reads, it
// goes through vault.Collect so `scope: private` is enforced on the read path, and it is
// read-only unless explicitly started with write enabled.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joeykokinda/aimem/internal/config"
	"github.com/joeykokinda/aimem/internal/search"
	"github.com/joeykokinda/aimem/internal/vault"
)

// Version is reported to clients during initialize.
const Version = "0.2.0"

// defaultProtocol is used when a client does not name a protocol version. When one does,
// its version is echoed back: this server's surface is small enough to be compatible
// across the versions in use.
const defaultProtocol = "2024-11-05"

// Server answers MCP requests against one vault.
type Server struct {
	settings *config.Config
	writable bool
	in       io.Reader
	out      io.Writer
}

func New(settings *config.Config, writable bool) *Server {
	return &Server{settings: settings, writable: writable, in: os.Stdin, out: os.Stdout}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the stdio loop until the client closes the connection.
//
// Nothing is ever written to stdout except protocol messages: stdout is the transport,
// so a stray print corrupts the session. Diagnostics go to stderr.
func (s *Server) Serve() error {
	reader := bufio.NewReaderSize(s.in, 1<<20)
	decoder := json.NewDecoder(reader)
	for {
		var incoming request
		if err := decoder.Decode(&incoming); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		// Notifications have no id and take no response.
		if len(incoming.ID) == 0 {
			continue
		}
		result, failure := s.dispatch(incoming)
		reply := response{JSONRPC: "2.0", ID: json.RawMessage(incoming.ID)}
		if failure != nil {
			reply.Error = failure
		} else {
			reply.Result = result
		}
		encoded, err := json.Marshal(reply)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(s.out, "%s\n", encoded); err != nil {
			return err
		}
	}
}

func (s *Server) dispatch(incoming request) (interface{}, *rpcError) {
	switch incoming.Method {
	case "initialize":
		return s.initialize(incoming.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools()}, nil
	case "tools/call":
		return s.call(incoming.Params)
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + incoming.Method}
	}
}

func (s *Server) initialize(params json.RawMessage) map[string]any {
	protocol := defaultProtocol
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &request) == nil && request.ProtocolVersion != "" {
		protocol = request.ProtocolVersion
	}
	mode := "read-only"
	if s.writable {
		mode = "read-write"
	}
	return map[string]any{
		"protocolVersion": protocol,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "aimem", "version": Version},
		"instructions": fmt.Sprintf(
			"Memory for the %q vault, served %s.\n\n"+
				"Call vault_context first: one read covers every company, project, research thread, "+
				"and repo path. Use vault_search before grepping, and vault_note to read one note in full.\n\n"+
				"Private folders (%s) are not reachable through this server by design. Do not ask for "+
				"them and do not try to read them another way.",
			s.settings.Name, mode, strings.Join(s.settings.Private, ", ")),
	}
}

func (s *Server) tools() []map[string]any {
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}

	tools := []map[string]any{
		{
			"name": "vault_context",
			"description": "The cross-project index: every company, project, research thread, idea, " +
				"and repo path, with status. Read this before asking what a project is or searching " +
				"the filesystem. Pass `project` to get one note's full text plus its recent timeline " +
				"instead of the whole index.",
			"inputSchema": object(map[string]any{
				"project": text("Optional note title to scope to, e.g. \"Aimem\"."),
			}),
		},
		{
			"name": "vault_search",
			"description": "Ranked search across the shared vault, with frontmatter filters. " +
				"Prefer this over grep: it scores title and tag matches above body matches and " +
				"understands type/status/company/tag.",
			"inputSchema": object(map[string]any{
				"query":   text("Words to search for. All must match. May be empty if filtering."),
				"type":    text("Filter by frontmatter type, e.g. project, company, research, idea."),
				"status":  text("Filter by status, e.g. active, paused, shipped, dead, evergreen."),
				"company": text("Filter by owning company."),
				"tag":     text("Filter by a single tag."),
				"limit":   map[string]any{"type": "integer", "description": "Max results (default 10)."},
			}),
		},
		{
			"name":        "vault_note",
			"description": "Read one note in full by title or vault-relative path.",
			"inputSchema": object(map[string]any{
				"name": text("Note title, e.g. \"Aimem\", or a vault-relative path."),
			}, "name"),
		},
		{
			"name": "vault_repo",
			"description": "Given an absolute path to a checkout on disk, return the vault note " +
				"describing it. Use this when you start work in an unfamiliar repository.",
			"inputSchema": object(map[string]any{
				"path": text("Absolute path to a repository."),
			}, "path"),
		},
	}

	if s.writable {
		tools = append(tools, map[string]any{
			"name": "vault_remember",
			"description": "Append a durable fact to a note's agent-memory section. Use only for " +
				"things worth knowing weeks from now: decisions and their reasoning, non-obvious " +
				"architecture, recurring gotchas, external constraints. Never for secrets, one-off " +
				"debugging steps, or anything the code or git history already says.",
			"inputSchema": object(map[string]any{
				"note": text("Note title to append to."),
				"fact": text("One sentence. Durable, specific, and not derivable from the repo."),
			}, "note", "fact"),
		})
	}
	return tools
}

func (s *Server) call(params json.RawMessage) (interface{}, *rpcError) {
	var request struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}

	body, err := s.run(request.Name, request.Arguments)
	if err != nil {
		// Tool failures are returned as content with isError, not as protocol errors, so
		// the model can read what went wrong and adjust.
		return toolResult(err.Error(), true), nil
	}
	return toolResult(body, false), nil
}

func toolResult(body string, failed bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": body}},
		"isError": failed,
	}
}

func (s *Server) run(name string, arguments map[string]any) (string, error) {
	switch name {
	case "vault_context":
		return s.context(stringArg(arguments, "project"))
	case "vault_search":
		return s.search(arguments)
	case "vault_note":
		return s.note(stringArg(arguments, "name"))
	case "vault_repo":
		return s.repo(stringArg(arguments, "path"))
	case "vault_remember":
		if !s.writable {
			return "", fmt.Errorf("this server is read-only; restart it with --write to enable vault_remember")
		}
		return s.remember(stringArg(arguments, "note"), stringArg(arguments, "fact"))
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *Server) notes() ([]*vault.Note, error) {
	return vault.Collect(s.settings, s.settings.IndexFolders())
}

func (s *Server) context(project string) (string, error) {
	if project == "" {
		raw, err := os.ReadFile(s.settings.MetaPath("BRAIN.md"))
		if err != nil {
			return "", fmt.Errorf("no index yet; run `aimem refresh` (%w)", err)
		}
		return string(raw), nil
	}

	notes, err := s.notes()
	if err != nil {
		return "", err
	}
	note := search.Resolve(notes, project)
	if note == nil {
		return "", fmt.Errorf("no note named %q; call vault_search to find it", project)
	}
	var out strings.Builder
	out.WriteString(fmt.Sprintf("# %s\n\n%s\n", note.Title, note.Body))
	if timeline := s.timeline(note.Title); timeline != "" {
		out.WriteString("\n## Recent activity\n\n" + timeline)
	}
	return out.String(), nil
}

// timeline pulls one project's section out of the generated Activity note. The journal
// itself is never opened here: this reads only what the audited bridge already published.
func (s *Server) timeline(title string) string {
	raw, err := os.ReadFile(s.settings.MetaPath("Activity.md"))
	if err != nil {
		return ""
	}
	heading := fmt.Sprintf("## [[%s]]", title)
	var collected []string
	capturing := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## ") {
			capturing = line == heading
			continue
		}
		if capturing && strings.TrimSpace(line) != "" {
			collected = append(collected, line)
		}
	}
	return strings.Join(collected, "\n")
}

func (s *Server) search(arguments map[string]any) (string, error) {
	notes, err := s.notes()
	if err != nil {
		return "", err
	}
	limit := 10
	if value, ok := arguments["limit"].(float64); ok && value > 0 {
		limit = int(value)
	}
	hits := search.Run(notes, search.Query{
		Terms:   strings.Fields(stringArg(arguments, "query")),
		Type:    stringArg(arguments, "type"),
		Status:  stringArg(arguments, "status"),
		Company: stringArg(arguments, "company"),
		Tag:     stringArg(arguments, "tag"),
		Limit:   limit,
		Context: 3,
	})
	if len(hits) == 0 {
		return "No matches in the shared vault.", nil
	}
	encoded, err := json.MarshalIndent(hits, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *Server) note(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	notes, err := s.notes()
	if err != nil {
		return "", err
	}
	note := search.Resolve(notes, name)
	if note == nil {
		return "", fmt.Errorf("no note named %q in the shared vault; call vault_search to find it", name)
	}
	return fmt.Sprintf("# %s\n`%s`\n\n%s", note.Title, note.Path, note.Body), nil
}

func (s *Server) repo(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	notes, err := s.notes()
	if err != nil {
		return "", err
	}
	// Most specific wins, and a note with no repo matches nothing: an empty Repo would
	// turn the prefix test into HasPrefix(path, "/"), which every absolute path passes.
	var best *vault.Note
	for _, note := range notes {
		if note.Repo == "" {
			continue
		}
		if note.Repo != path && !strings.HasPrefix(path, note.Repo+"/") {
			continue
		}
		if best == nil || len(note.Repo) > len(best.Repo) {
			best = note
		}
	}
	if best != nil {
		return fmt.Sprintf("# %s\n`%s`\n\n%s", best.Title, best.Path, best.Body), nil
	}
	return "", fmt.Errorf("no vault note claims %q", path)
}

func (s *Server) remember(name, fact string) (string, error) {
	if name == "" || fact == "" {
		return "", fmt.Errorf("both note and fact are required")
	}
	notes, err := s.notes()
	if err != nil {
		return "", err
	}
	note := search.Resolve(notes, name)
	if note == nil {
		return "", fmt.Errorf("no note named %q", name)
	}
	if err := vault.Remember(note, fact); err != nil {
		return "", err
	}
	return fmt.Sprintf("Appended to %s. Run `aimem refresh` to rebuild the index.", note.Path), nil
}

func stringArg(arguments map[string]any, key string) string {
	if value, ok := arguments[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// SetTransport redirects a server's streams. Exported for tests, which drive the real
// JSON-RPC surface rather than calling the tool implementations directly: the protocol
// layer is part of what has to not leak.
func SetTransport(s *Server, in io.Reader, out io.Writer) {
	s.in, s.out = in, out
}
