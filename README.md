# aimem

Local-first memory for people and coding agents.

A plain Markdown vault (Obsidian or not) is the human-facing second brain. `aimem` is the
deterministic retrieval layer on top of it: it gives an AI agent useful project context in
one read, and it never hands over the half of your notes that are none of its business.

There is no database, no embedding index, and no network call. Your notes stay Markdown
files in a git repository you own.

```
aimem init --vault ~/notes     # scaffold a vault and its config
aimem refresh                  # validate, build the index, sync repo context
aimem context                  # the whole cross-project index, one read
aimem find hedera consensus    # ranked search across the shared tier only
```

## Why this exists

An agent dropped into a repository knows the code and nothing else. It does not know why
you chose this database, which company owns this service, what you already tried, or that
the thing it is about to suggest failed last month. That context exists, in your notes,
and the obvious ways to give it over are both bad:

- **Paste it into a prompt.** It goes stale the day you write it.
- **Point a filesystem MCP server at your notes folder.** Now the agent can read your
  journal, your finances, and your medical appointments, and nothing stops it.

aimem is the third option. One generated index file answers "what is everything I work
on", a ranked search answers "what did we decide about X", and a **folder tier system**
makes the private half structurally unreachable rather than politely off-limits.

## The privacy boundary

Every folder in your vault is in exactly one tier, declared in one config file:

| Tier | Behavior |
|---|---|
| **shared** | Indexed, searchable, served to agents |
| **private** | Never opened, except by the audited journal bridge below |
| **locked** | Never opened at all, mounted or not |
| **meta** | Holds the generated index; validated but not indexed |

This is enforced in code, not documented as a convention:

- `vault.Collect` **refuses** to walk a private folder and returns an error. A caller bug
  cannot silently turn into a leak.
- A note in a shared folder can set `scope: private` to drop out. Scope narrows; it never
  widens. A private note cannot opt in.
- The config loader **rejects** a folder listed in two tiers, a folder name containing a
  path separator or `..`, and a journal folder outside the private tier.
- The folder tiers are **required** in the config, never defaulted. A config section that
  fails to parse fails the load, because silently substituting built-in names would leave
  your real private folders in no tier at all.

`internal/boundary` plants a canary string in every private and locked file, then asserts
it appears in **no** output surface: the index, the timeline, the JSON, the validator's
own report, ranked search, and every MCP tool response, including responses to arguments
that ask for private paths directly. That suite runs as its own CI job. It is the claim
you have to trust before adopting this, so it is the claim that is tested hardest.

What this does **not** do: sandbox other processes. Folder scoping stops *these tools*
from reading private notes. It does not stop anything else running as your user. Real
secrets belong in a secret manager or a hardware wallet, never in a vault.

## Serving a vault to an agent

```bash
aimem mcp --vault ~/notes
```

This is an MCP server exposing four read-only tools: `vault_context`, `vault_search`,
`vault_note`, and `vault_repo`. Register it with any MCP client:

```bash
claude mcp add mybrain --scope user -- aimem mcp --vault ~/notes
```

**Prefer this over a generic filesystem MCP server**, which is the usual approach and
quietly undoes most of the guarantees above. A filesystem server ignores `scope: private`,
restates your folder allowlist in a second place where it drifts, and exposes write, move,
and delete alongside read. `aimem mcp` reads the same config as every other command, goes
through the same collector, and is read-only unless started with `--write`.

With `--write`, one more tool appears: `vault_remember`, which appends a dated fact to a
note. It scans for secrets first and refuses to write one, since that is the only path
where agent-composed text enters a repository you push.

## Derived data, not stored data

Two fields that rot when maintained by hand are computed instead.

**Last updated** comes from `git log`, not a frontmatter `updated:` field. Hand-kept dates
depend on someone remembering, so the notes that most need an accurate date have the most
wrong one. Filesystem mtime is destroyed by a clone or a backup. Git already tracks it
correctly, for free. Nothing is written back into frontmatter on purpose: stamping every
note on every refresh would churn the whole vault and make the commit that records a date
change the date it records.

**Staleness** is `status: active` plus N days of silence, counting both note edits and
journal mentions. The point is to make `status:` falsifiable. When every note is active
forever the field carries no information and the dashboards querying it return everything.

Staleness applies only to the types you list in `staleable_types`, normally projects and
companies, where `active` is a claim about work in progress. Ideas and research are
exempt: they accrete rather than rot, and warning about them would train you to ignore the
warning that matters.

## The journal bridge

Your daily journal is private, and it is also where progress actually gets written.
Sealing it off entirely also seals off the one signal that keeps the index current.

`internal/activity` is the narrow, audited bridge. Its contract:

- It reads the single folder named by `folders.journal` and nothing else. That folder must
  be in the private tier, or extraction is refused. The bridge narrows private access; it
  can never grant it.
- A line is emitted **only** if it links to a note that is already shared. A line with no
  wikilink, or one linking only to private notes, never leaves the folder.
- A line tagged `#private` is dropped. `%%Obsidian comments%%` are stripped.
- Every emitted line is scanned for secrets and dropped if it matches.
- Only counts are printed, never prose. The caller is often an agent.

Extracted lines land in the meta folder and **are readable by any agent with vault
access.** That is the deliberate trade: a timeline no agent can see is not worth deriving.
`#private` and `%%...%%` are how a line stays out. Set `journal: ""` to disable the bridge.

### Writing into it

The link requirement is what makes the bridge safe, and it is also why it sits idle: it
asks you to type `[[Brackets]]` at the moment you are least inclined to. `aimem log`
closes that from the other side.

```bash
aimem log deposit sweep works on omenswap now
# - deposit sweep works on [[Omenswap|omenswap]] now
#   reaches the timeline via Omenswap
```

It finds the project names in your sentence, writes the links, preserves the casing you
typed, and tells you when a line will *not* reach the timeline so the silence is never a
surprise. `--project <Title>` forces a link when the sentence does not name it.

Writing to the journal is **CLI-only and deliberately not an MCP tool.** An agent that
could write into the private journal could stage its own text there and have the next
refresh publish it into shared output, turning a one-way valve into a laundering channel.

`aimem refresh` also reports which shared notes your journal named *without* linking, so
an empty timeline is distinguishable from an uneventful week:

```
note: journal lines named these without linking them, so they did not
      reach the timeline: Omenswap (2), Crypto-Bootcamp (1)
```

Only note titles are reported, never the surrounding prose, and lines tagged `#private`
are excluded from the count so the diagnostic cannot reveal what an opted-out line was
about.

## Install

Requires Go 1.22+. No third-party dependencies, at build time or runtime.

```bash
go install github.com/joeykokinda/aimem/cmd/aimem@latest
```

Or from a checkout:

```bash
git clone https://github.com/joeykokinda/aimem
cd aimem && ./install.sh
```

Then point it at a vault:

```bash
aimem init --vault ~/notes    # new vault: scaffolds folders and writes the config
aimem use ~/notes             # existing vault that already has .aimem.yml
aimem refresh
aimem doctor                  # verify the install, the config, and the boundary
```

`init` adopts a folder layout that already exists rather than imposing one, ignoring any
numeric ordering prefix, so `04-Projects` is recognized as the projects folder.

## Configuration

One file, `.aimem.yml`, at the vault root. It lives there so the vault is
self-describing: clone it on another machine and aimem knows how to read it, with nothing
to recreate. Nothing about any particular vault is compiled into the binary.

```yaml
name: Notes

folders:
  shared: [Companies, Projects, Research, Ideas, Reference]
  private: [Inbox, Daily, Personal]
  locked: Locked
  meta: Meta
  journal: Daily          # must be listed in private; "" disables the bridge
  journal_filename: 2006-01-02   # Go time layout naming each daily note

schema:
  types: [project, company, research, idea, reference, journal, dashboard]
  statuses: [active, paused, shipped, dead, evergreen]
  staleable_types: [project, company]
  stale_days: 30
  journal_entries_per_project: 12
  index_repo_map: true    # the repo table is the largest block in the index

sync:
  enabled: true
  owners: [your-github-username]   # only write into repos whose remote you own
  skip: []
```

The one thing that cannot live in the vault is the vault's own location. `aimem use`
saves that single path to `$XDG_CONFIG_HOME/aimem/vault`; `--vault` and `$AIMEM_VAULT`
override it.

## Commands

| Command | What it does |
|---|---|
| `aimem context [project]` | The cross-project index, or one project in full |
| `aimem find <words>` | Ranked search, with `--type --status --company --tag` |
| `aimem show <note>` | Print one note by title or path |
| `aimem project` / `path` | The vault note for the repo you are standing in |
| `aimem activity [project]` | Timeline derived from the daily journal |
| `aimem stale` | Notes claiming active that nobody has touched |
| `aimem remember <fact>` | Append a durable fact to this repo's note |
| `aimem log <what you did>` | Add an auto-linked line to today's journal |
| `aimem refresh` | Validate, rebuild the index, sync repo context |
| `aimem validate` | Check the vault against its config |
| `aimem doctor` | Check the install, the config, and the boundary |
| `aimem config` | Print the resolved vault contract |
| `aimem mcp` | Serve the vault to agents over MCP |
| `aimem init` / `use` | Set up a vault, or point this machine at one |
| `aimem install-hooks` | Install the vault's pre-commit hook |

Most commands take `--json`.

### Search, not grep

`aimem find` scores title and tag matches above body matches, requires **all** terms to
match, and understands frontmatter:

```bash
aimem find consensus --type project --status active
aimem find --tag go --status active          # filters alone, no search terms
```

A vault is hundreds of notes, not millions. There is nothing here an embedding index would
find that ranked substring matching does not, and nothing a model would rank better.

## Keeping it honest

`aimem refresh` runs the validator first and lets it gate everything after. That order is
the point: regenerating the index from a broken vault propagates the breakage into every
agent's context at once.

The validator checks frontmatter validity against your configured types and statuses,
`repo:` paths that do not exist, broken wikilinks, duplicate titles, folder/type
agreement, stray code files, folders in no tier, whether the locked folder is gitignored,
hand-kept `updated:` fields, stale `active` claims, and nine secret formats. Errors fail
the run; warnings are advisory unless you pass `--strict`.

`aimem install-hooks` adds a vault pre-commit hook that validates and regenerates, so a
commit cannot leave the index drifting behind the notes. Bypass with `--no-verify`.

Between commits the index can still fall behind, so every read command warns on stderr
when a note is newer than the index. `aimem refresh --if-stale` does nothing when nothing
changed, which makes it cheap to run from a timer or a shell hook:

```bash
# fish, in config.fish
function aimem_sync --on-event fish_prompt
    command aimem refresh --if-stale --no-sync >/dev/null 2>&1 &
end
```

## Repo context blocks

`refresh` writes a generated block into the `CLAUDE.md` of every repository a note claims,
naming the vault note that describes it. The block sits between markers and is replaced in
place; anything you write around it survives.

Which repositories are yours to write into is derived from the git remote and the `owners`
list, not from a hardcoded path list. A list of absolute paths is inert on any other
machine, which means the guard against editing someone else's `CLAUDE.md` silently stops
guarding the moment a directory layout changes. A repository with no remote at all is
local-only work and counts as yours.

## What belongs in memory

Memory is only useful if it stays dense. The failure mode is a vault full of facts that
were true for one afternoon.

**Worth keeping:** decisions and the reasoning behind them, architecture that is not
obvious from the code, recurring gotchas and their fix, external constraints, status
changes.

**Not worth keeping:** anything git history or the code already says, one-off debugging
steps, and secrets of any kind.

## Development

```bash
./test.sh        # vet, gofmt, unit tests, and an end-to-end run through `aimem init`
make build
```

The module has no third-party dependencies, including the YAML parser and the MCP server.
A config file this shape is not worth a dependency, and the parser is smaller than the
dependency's changelog.

## Documentation

- [`docs/usage.md`](docs/usage.md) — the daily loop, what an agent should do on arrival,
  and how to stand this up on a new machine.

## License

MIT. See [LICENSE](LICENSE).
