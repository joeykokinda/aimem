# Using aimem

The README describes what aimem *is*. This describes how to work with it: the daily loop,
what an agent should do on arrival, what belongs in memory, and how to stand it up
somewhere new.

---

## The one-line version

The meta folder holds `BRAIN.md`, a generated index of the shared half of your vault.
Agents read it instead of crawling every note. `aimem` is the tool that reads, searches,
writes to, and regenerates it.

---

## For an agent arriving in a repository

1. **Read `aimem context` first.** One read gets every company, project, research thread,
   repo path, and status. Do this before asking what a project is or grepping the
   filesystem to find out. If you only need one project, `aimem context <name>` is the
   cheap version: one note instead of the whole index.

2. **Run `aimem project`** to get the note for the repo you are standing in. If it errors
   with "no vault note maps to…", this repo has no note yet. Either add a `repo:` field to
   an existing note or accept that it has no memory.

3. **Search with `aimem find`, not grep over the vault.** `find` is scoped to the shared
   tier and ranks title and tag matches above body matches. Reaching around it into the
   private folders defeats the only privacy boundary this system has, and the collector
   will refuse you anyway.

4. **Write durable conclusions back** with `aimem remember`. If you spent twenty minutes
   learning something the next agent would also spend twenty minutes learning, that is the
   bar.

A repo that has been through a sync carries an `aimem:begin` / `aimem:end` block in its
`CLAUDE.md` naming its vault note. Never hand-edit inside those markers; the next refresh
overwrites it. Everything outside them is yours.

---

## The daily loop

1. Capture quickly, in an inbox or a daily note.
2. Link work to its durable entity. `aimem log deposit sweep works on alpha` writes
   `- deposit sweep works on [[Alpha|alpha]]` for you and says whether the line will
   reach the timeline. A daily line with no wikilink to a shared note never leaves the
   journal, which is the safety property and also the thing that silently costs you a
   timeline if you forget.
3. Let backlinks build the project timeline automatically.
4. Promote only durable decisions, architecture, and recurring gotchas into project or
   research notes.
5. Review `aimem stale` and the "Unlinked Notes" section of `BRAIN.md` weekly, then
   `aimem refresh`.

---

## What to remember, and what not to

Memory is only useful if it stays dense. The failure mode is a vault full of facts that
were true for one afternoon.

**Worth remembering**

- Decisions and the reasoning behind them: "we picked X over Y because Z"
- Architecture that is not obvious from reading the code
- Recurring gotchas: the thing that breaks every time, and the fix
- External constraints: a vendor limit, an API quirk, a hard date
- Status changes: a project going paused, shipped, or dead

**Not worth remembering**

- Anything git history or the code already says
- One-off debugging steps that worked once
- Secrets of any kind. The vault is versioned and pushed. `aimem remember` and the
  MCP write tool both refuse text matching a known secret format, and the validator
  scans committed notes for nine more, precisely because this rule gets broken.

---

## Frontmatter

```yaml
---
type: project        # must be in schema.types
status: active       # must be in schema.statuses
company: Acme
repo: /absolute/path/to/repository
tags: [go, web3]
scope: private       # optional: drop this note from every shared surface
---
```

`type` and `status` drive how a note is grouped in `BRAIN.md`. `repo` is what makes
`aimem project` and the repo context sync work, and it is checked against the filesystem:
a note pointing at a repo that is not on disk is flagged in the index.

Never add `updated:`. It is derived from git, and the validator warns if it is present.

Every new note wants a `[[Wikilink]]` pointing *at* it from an existing note, usually its
folder README. `BRAIN.md` lists unreachable notes under "Unlinked Notes".

---

## Knowing who you are

```bash
aimem profile
```

Languages counted from the checkouts your notes claim, subjects from tags on notes touched
recently, and clusters of notes that share a subject. Nothing here is hand-maintained,
which is the point: a written "languages I know" list is wrong within a year, and an agent
reading a stale one is worse off than one that read nothing. The same content appears at
the top of the index, so agents get it without asking.

What cannot be derived is access: which accounts exist, which services are paid for, what
hardware is on hand. Put those in an ordinary reference note and tag it `access`, `host`,
`hardware`, or `account`; the profile will point at it without trying to guess the contents.

## Getting context into a session automatically

```bash
aimem brief --hook
```

Roughly 1KB covering what you write, what you work on, what is active, the note for the
repo the session started in, and anything waiting for review. Wire it to Claude Code's
`SessionStart` hook and agents arrive already knowing, rather than only knowing if they
thought to ask.

Keep it small on purpose: it is paid for on every session, so it carries the shape of
things and pointers to the detail, never the detail itself.

## Reviewing what agents wrote

With `aimem mcp --write`, agents can append facts and create notes without asking. That is
safe because of where the writes land:

```bash
aimem review                   # what is waiting
aimem review --promote         # accept everything
aimem review --drop            # discard everything
aimem review --note Starling   # one note at a time
```

An agent-written fact looks like `- 2026-09-30 (unreviewed): ...`. An agent-created note
carries `origin: agent`. Neither reaches `BRAIN.md` until promoted; an unreviewed note is
*named* in the index so you know review is waiting, never described. Promoting a fact drops
the marker; dropping deletes the line.

The reasoning is that junk on disk costs nothing and junk in the index costs on every turn.
So capture is free and triage is something you do when you feel like it. Writing the same
fact twice is refused, which is the most common way an agent manufactures junk.

Journal writing is deliberately not an MCP tool, so an agent cannot stage text in your
private journal for the next refresh to publish.

## Searching

```bash
aimem find hedera consensus              # all terms must match
aimem find --type project --status active   # filters with no search terms
aimem find retry --tag go --limit 5
aimem find consensus --json              # structured, for scripts and agents
```

Ranking is BM25 over stemmed words, weighted by field. Practically that means `deployment`
finds a note about `deploying`, a title match beats a body match, a rare word counts for
more than a common one, and repeating a word stops helping after a few occurrences.

Words that are interchangeable in *your* vault go in the config, because nothing general
can guess them:

```yaml
schema:
  synonyms:
    - auth, authentication, login, signin
    - deploy, deployment, release, rollout
```

Pick these carefully. A group containing `ship` matched a shipping-label note when the
query was `deployment`, which is exactly the failure mode a built-in list would have had
everywhere.

---

## Serving it to agents

```bash
claude mcp add mybrain --scope user -- aimem mcp --vault ~/notes
```

Read-only by default, exposing `vault_context`, `vault_search`, `vault_note`, and
`vault_repo`. Add `--write` to also expose `vault_remember`.

Do not register a generic filesystem MCP server against the same folders. It ignores
`scope: private`, duplicates your folder allowlist somewhere it will drift, and adds
write, move, and delete to a surface that should be read-only.

---

## Temporarily widening access

`scripts/grant-private.sh` registers a separate filesystem MCP server exposing the private
folders; `scripts/revoke-private.sh` removes it. The folder list comes from the vault
config, so it cannot drift, and the locked folder is never included even when mounted.

This is deliberately a different command rather than a flag on `aimem mcp`. If widening
the boundary were one argument away, the boundary would be a default rather than a
property. Use it, then revoke it.

`scripts/locked.sh init|unlock|lock|status` manages an encrypted section with gocryptfs.
Ciphertext lives outside the vault so neither git nor Obsidian's indexer sees it. While
unlocked, **any process running as your user can read it**, including an agent. Unlock,
work, lock again.

---

## Keeping it honest

`aimem refresh` runs the validator first and lets it gate everything after. That order is
the point: regenerating from a broken vault propagates the breakage into every agent's
context at once.

`aimem doctor` is the one to run when something feels wrong. It checks the config loads,
the shared folders exist, the locked folder is gitignored, the index has been built, the
schema validates, and that the collector genuinely refuses to walk a private folder.

Run `./test.sh` after changing anything. It vets, checks formatting, runs the unit tests,
and then does a full end-to-end run through `aimem init` against a throwaway vault, which
is the path most likely to rot unnoticed.

---

## Bootstrapping a new machine

```bash
go install github.com/joeykokinda/aimem/cmd/aimem@latest
git clone <your vault remote> ~/notes
aimem use ~/notes
aimem refresh
aimem install-hooks
```

That is the whole procedure. The vault config travels with the vault, so there is nothing
to recreate; `aimem use` records the one thing that cannot live inside the vault, which is
where the vault is.

Write `repo:` fields relative to `code_root` so they resolve on every machine:

```yaml
code_root: ~/Projects     # .aimem.yml
repo: company/meridian     # a note
```

An absolute `repo:` still works but names exactly one machine. `aimem portable` converts
an existing vault, and only rewrites a path when the shorter form resolves back to the
same directory. `aimem validate` warns about the ones left over.

---

## Host notes

A vault is portable and per-person; a machine is neither. Facts like "which service owns
port 3001 on this box" or "which tunnels are public" are real context an agent needs, but
they are not projects.

Put them in the vault as a reference note named for the host, with `type: reference` and a
`host` tag, so they are indexed and reachable from `aimem context` and `aimem find` like
everything else. A host note earns its place by recording what you cannot re-derive in ten
seconds: which of four supervisors owns a service, which tunnels are public, what was
decommissioned and when. Not `uptime`.

The same rule applies as everywhere else: no tokens, passwords, or seeds, only the path to
where they are kept.
