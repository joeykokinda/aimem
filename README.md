# aimem

Give your AI coding agent access to your notes, without giving it your journal.

You keep notes in Markdown (Obsidian or anything else). aimem builds an index of them, lets you search them, and serves them to agents over MCP. Folders you mark private are never read.

One Go binary. No database, no embeddings, no network calls, no dependencies.

## Quick start

```bash
go install github.com/joeykokinda/aimem/cmd/aimem@latest

aimem init --vault ~/notes   # creates folders and a config file
aimem refresh                # builds the index
aimem context                # prints it
```

To let Claude Code read your notes:

```bash
claude mcp add notes --scope user -- aimem mcp --vault ~/notes
```

## The problem it solves

An agent in your repo can read the code. It can't know that you already tried this approach, that the service belongs to a different company, or what you decided last month.

That information is in your notes. But the usual way to share it, pointing a filesystem MCP server at your notes folder, gives the agent everything: your journal, your finances, your medical stuff.

aimem splits your vault in two. Agents get the project half. The personal half is unreachable.

## How folders work

Every folder is in one of four tiers. You set this in one config file.

| Tier | What happens |
|---|---|
| `shared` | Indexed, searchable, agents can read it |
| `private` | Never opened |
| `locked` | Never opened, even if it's mounted |
| `meta` | Holds the generated index |

Example config (`.aimem.yml`, at the root of your vault):

```yaml
name: Notes
code_root: ~/Projects     # repo: paths may be written relative to this

folders:
  shared: [Companies, Projects, Research, Ideas, Reference]
  private: [Inbox, Daily, Personal]
  locked: Locked
  meta: Meta
  journal: Daily
  journal_filename: 2006-01-02

schema:
  types: [project, company, research, idea, reference, journal, dashboard]
  statuses: [active, paused, shipped, dead, evergreen]
  staleable_types: [project, company]
  stale_days: 30
  index_repo_map: true

sync:
  enabled: true
  owners: [your-github-username]
```

The config lives in the vault, so if you clone your notes on another machine, everything still works. You just run `aimem use ~/notes` to say where they are.

## What makes the privacy boundary real

It's enforced in code, not just documented:

- Asking the collector to read a private folder returns an error, not an empty result.
- A note in a shared folder can add `scope: private` to its frontmatter and disappear from everything, including MCP.
- The config won't load if a folder is in two tiers, if a folder name contains `..`, or if the folder tiers are missing.
- The MCP server is read-only unless you pass `--write`.

There's a test suite that puts a marker string in every private file, then checks that it never shows up in the index, the timeline, the JSON, search results, or any MCP response, including when a request asks for a private path directly. It runs as its own CI job.

**What it does not do:** stop other programs. This scopes what *these tools* read. Anything else running as your user can still read your files. Keep real secrets in a password manager.

## Commands

| Command | What it does |
|---|---|
| `aimem context [project]` | Print the index, or one project |
| `aimem find <words>` | Search. Supports `--type --status --company --tag` |
| `aimem show <note>` | Print one note |
| `aimem project` | Which note describes the repo you're in |
| `aimem log <what you did>` | Add a line to today's journal, auto-linked |
| `aimem remember <fact>` | Save a fact to this repo's note |
| `aimem profile` | What you work on, what you write, how it connects |
| `aimem review` | Triage what agents wrote: promote or drop |
| `aimem activity [project]` | What happened recently, from your journal |
| `aimem stale` | Active notes nobody has touched |
| `aimem refresh` | Rebuild everything |
| `aimem validate` | Check your notes for problems |
| `aimem doctor` | Check that the whole setup works |
| `aimem mcp` | Serve your notes to agents |
| `aimem portable` | Rewrite absolute repo paths so the vault works anywhere |
| `aimem init` / `use` | Set up a vault, or point at one |

Most commands take `--json`.

## Using one vault on several machines

Your notes are a git repo, so syncing them is `git pull`. Two things make the *tooling*
work on the other machine:

```bash
git clone <your notes remote> ~/notes
aimem use ~/notes        # the only per-machine setup
aimem refresh
```

The config lives in the vault, so there is nothing to recreate.

The part that used to break is `repo:` paths. An absolute path names exactly one machine,
so a vault written on your laptop resolves to nothing on your desktop. Write them
relative to `code_root` instead:

```yaml
code_root: ~/Projects      # in .aimem.yml

repo: company/meridian      # in a note -> ~/Projects/company/meridian on any machine
repo: ~/Code/thing         # home-relative also works
repo: /opt/thing           # absolute still works, but only on one machine
```

To convert an existing vault:

```bash
aimem portable             # show what would change
aimem portable --write     # apply
```

It only rewrites a path when the shorter form resolves back to the same directory, so it
cannot silently repoint a note at a different checkout. `aimem validate` warns about any
absolute path left over.

## Notes format

Frontmatter on each note:

```yaml
---
type: project
status: active
company: Acme
repo: /path/to/the/code
tags: [go, web3]
---
```

`type` and `status` control how the note is grouped in the index. `repo` is what makes `aimem project` work.

Don't add an `updated:` field. aimem gets the date from git, which is always right.

## Searching

```bash
aimem find hedera consensus                 # both words must match
aimem find --type project --status active   # filter, no search words
aimem find retry --tag go --limit 5
```

Notes whose *title* matches rank above notes that just mention the word. Tags and frontmatter are searchable.

## The journal

Your daily notes are private. But that's where you actually write what you did, so aimem has a narrow way to get project updates out of them.

A journal line is copied into the shared timeline **only if it links to a note agents can already see**. Everything else stays private. Lines tagged `#private` are dropped, `%%comments%%` are stripped, and anything that looks like a secret is blocked.

Typing `[[Brackets]]` every time is annoying, so:

```bash
$ aimem log deposit sweep works on starling now
  - deposit sweep works on [[Starling|starling]] now
  reaches the timeline via Starling
```

It finds the project name, adds the link, and tells you when a line *won't* make it into the timeline.

`aimem refresh` also tells you which projects you wrote about without linking:

```
note: journal lines named these without linking them, so they did not
      reach the timeline: Starling (2), Ledger-Course (1)
```

Writing to your journal is CLI-only on purpose. It's not an MCP tool, so an agent can't put text in your journal and have it published on the next refresh.

## Letting agents write

Start the MCP server with `--write` and agents get `vault_remember` and
`vault_create_note`. They can write freely, because **everything an agent writes is
marked and held out of the index until you promote it**:

```bash
aimem review                  # see what is waiting
aimem review --promote        # accept it all
aimem review --drop           # discard it all
aimem review --note Starling  # one note at a time
```

The reasoning: junk on disk costs nothing. Junk in the file every session loads costs on
every turn. So capture is free and triage is something you do when you feel like it, not a
gate on writing things down.

An agent-written fact looks like `- 2026-09-30 (unreviewed): ...` and promoting it just
drops the marker. An agent-created note carries `origin: agent` and is named but not
described in the index until promoted. Writing the same fact twice is refused, which is
the most common way an agent generates junk.

Writing to the journal is deliberately *not* an MCP tool, so an agent cannot stage text in
your private journal for the next refresh to publish.

## Keeping the index current

`aimem refresh` checks your notes for errors first and stops if it finds any, so a broken note can't get baked into the index.

Install a git hook so committing your vault always rebuilds the index:

```bash
aimem install-hooks
```

Between commits, read commands warn you if a note is newer than the index. To rebuild automatically, `aimem refresh --if-stale` does nothing when nothing changed:

```fish
# fish, in config.fish
function aimem_sync --on-event fish_prompt
    command aimem refresh --if-stale --no-sync >/dev/null 2>&1 &
end
```

## Repo context blocks

`aimem refresh` adds a short block to the `CLAUDE.md` of every repo that a note points at, saying which note describes it. Your own text in that file is left alone.

It only writes to repos you own, based on the git remote and the `owners` list in your config. A repo with no remote counts as yours.

## What to save

Worth saving: decisions and why you made them, architecture that isn't obvious from the code, bugs that keep coming back, external limits, status changes.

Not worth saving: anything git already tells you, one-off debugging, and secrets of any kind.

## Development

```bash
./test.sh     # vet, format check, tests, and a full end-to-end run
make build
```

No third-party dependencies, including the YAML parser and the MCP server.

## Docs

[`docs/usage.md`](docs/usage.md) covers the daily workflow and setting up a new machine.

## License

MIT.
