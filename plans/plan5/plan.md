# Plan 5: Setup guide for AI agents

**Status:** Completed
**Created:** 2026-10-09 · **Finalized:** 2026-10-09 (user request: "Add an agents read me, so that the Claude Code agent can set it up for the user, downloads, walkthrough, etc.")

## 1. Goal

An agent such as Claude Code can set ragalay up for a non-technical user from one document: ask the user's choices, download and verify the binary, init, install the model, run the first index, check search, connect MCP, and show the user around.

## 2. Decisions

| # | Decision |
|---|---|
| D1 | File: `SETUP-FOR-AGENTS.md` at the repo root, not `AGENTS.md`. Coding agents load `AGENTS.md` automatically when they work on this repo, and these steps are for agents *using* ragalay. |
| D2 | Shipped in the release archives next to `README.md`, so an agent that unpacked ragalay finds it. |
| D3 | Pointed to from the README twice: from "Get started" (for a person who wants their agent to do setup) and from "For AI agents and power users". |
| D4 | Steps each end on a checkable "Done when". Facts that `--help` already gives stay out, except the gotchas a headless agent hits. |

## 3. Findings while writing (verified on Windows 11, 2026-10-09)

- Without a terminal, `setup` prints the download plan and exits 4 instead of asking. Agents use that as a preview, then pass `--yes`.
- `model use jina-v5` before setup saves the setting, then exits 4 at the license question.
- A missing consent for a remote service makes `scan` exit 1 (not 4), with the size and host in the message.
- `folders add <relative path> --root X` resolves against the current directory, not X. The guide uses absolute paths. (A possible fix for a later plan: resolve relative to the root.)
- `search --json` leaves empty fields out, and the README example showed them as `""`. Fixed. The `notice` goes to stderr as `note: …`.
- `releases/latest/download/...` returns 404 while the repo is private and the only release (v0.2.0) is a pre-release. The guide gives a `gh release download <tag>` fallback.

## 4. Phases

### Phase 1: Guide and README ✅ Completed (2026-10-09)

- [x] Write `SETUP-FOR-AGENTS.md`: ask, download + checksum (sh and PowerShell), init/folders/model, setup, first index, check search, MCP/CLI connection, user walkthrough, agent search reference.
- [x] Verify the commands headless: init, folders add, setup, scan `--json`, search `--json` on a scratch folder, and the checksum/unpack snippets against the v0.2.0 assets.
- [x] README: pointers to the guide, correct the `search --json` example.
- [x] `.goreleaser.yaml`: bundle the guide in the archives.

**Exit criteria:** every command in the guide ran as described on Windows (the macOS/Linux checksum line was checked with `sha256sum` against the Linux archive), and the README links resolve.
