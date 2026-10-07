# CLAUDE.md

## Project

`ragalay` is a Go CLI with a small TUI. It ingests Markdown, PDF, and image files (audio and video come in Plan 3), chunks and embeds them, and stores the vectors plus a BM25 index in Turso. Each folder picks an embedding profile: EmbeddingGemma 2 (default) or Jina v5 run locally (indexing in a Python sidecar, query embedding with the profile's GGUF through llama.cpp), or `openai`, any OpenAI-compatible `/v1/embeddings` service (no Python, no downloads). AI agents search through the CLI (`--json`) and MCP. People search through the TUI. You copy the binary into a directory and it works on that directory.

- Module: `github.com/satlavida/ragalay` (Go 1.26). `spikes/` is a separate throwaway module (Phase 0 experiments)
- Layout: `cmd/ragalay` (main) → `internal/cli` (cobra commands) → `internal/config` (config.toml, root discovery, folders) and `internal/store` (Turso schema, migrations, queries, query cache)
- Models: `internal/embed` (profiles in `profiles.go`, shared types, vector helpers), `internal/embed/openai` (OpenAI-compatible client and image extensions), `internal/setup` (machine-wide install per profile into the user cache; every download pinned in `pins.go`; `--prune`), `internal/sidecar` (Python indexing process for the local profiles; `sidecar.py` is embedded), `internal/llama` (in-process query embedding via yzma), `internal/download` (pinned, resumable downloads). `internal/cli/models.go` builds the index/query runtimes for a profile
- Model switches: `internal/index/space.go` + `internal/store/shadow.go`. A new vector space is built in `*_next` shadow tables and swapped in when complete; search reads only the live tables with the live model (`index.LiveEmbed`). Chunk writes go through `store.WriteTarget`
- Indexing: `internal/scan` (walk, change/move detection, MD↔PDF pairing, watch) under `internal/lock` (single indexer). Anything that writes the index must hold the lock
- Search: `internal/search` (hybrid vector + BM25 with RRF, no Python), `internal/mcpserver` (MCP tools), `internal/tui` (Bubble Tea UI behind a `Backend` interface implemented in `internal/cli/tui.go`)
- Releases: `.goreleaser.yaml` + `.github/workflows/release.yml` on `v*` tags; `internal/update` installs them. Archive names are part of the update contract (`update.AssetName`)
- `go test ./...` includes `TestBothRuntimesOnThisMachine` (per installed local profile), `TestDoubleClickToSearch` and `TestTUIBackendSwitchesModels`, which use the real models when `ragalay setup` has run (skipped otherwise, or with `-short`)
- Schema migrations run whenever a command opens a folder (`cli.upgradeIndex`), not only at `init`
- Database access always goes through `store.With` (short-lived connection + lock retry; Turso locks the file per process on Windows)
- Entry point: `cmd/ragalay/main.go`
- Build: `go build ./...` / Run: `go run ./cmd/ragalay`
- Test: `go test ./...`

## Plans workflow (always follow)

All work is driven by plans in `plans/`. Read this folder before starting any task.

```
plans/
  plan1/plan.md     # active plan
  plan2/plan.md     # next plan, numbered in order
  archive/          # fully completed plans get moved here
```

1. **Find the active plan.** It is the lowest-numbered `plans/planN/` folder that is not in `archive/`. Read its `plan.md` before writing code.
2. **Read and refine.** A new or draft plan gets read critically and improved: fill gaps, flag risks, check external facts. If anything is ambiguous or is the user's call, put it under "Open questions" and **ask the user** before you finalize. Don't guess on decisions that are theirs.
3. **Finalize, then split into phases.** Set the plan's status to `Finalized` only after the user has answered the open questions. Then split it into ordered phases. Each phase gets a goal, a task checklist (`- [ ]`), and exit criteria you can check.
4. **Track progress in the document.** Tick tasks (`- [x]`) as you finish them. When every task and exit criterion in a phase is met, mark the phase heading `✅ Completed (YYYY-MM-DD)`. Update the plan's top-level status as you go: `Draft` → `Finalized` → `In progress` → `Completed`.
5. **Commit after each phase.** When a phase is marked completed, make one git commit holding that phase's code plus the updated `plan.md`. Message: `planN phase M: <phase title>`. Don't push unless the user asks.
6. **Archive and tag.** When every phase is complete, set the status to `Completed`, move the whole folder (`plans/planN` → `plans/archive/planN`), commit (`planN complete`), and create an annotated git tag on that commit: `git tag -a planN -m "Plan N complete: <plan title>"`. Don't push tags unless the user asks.
7. **New work = new plan.** Put work that falls outside the active plan in the next `plans/planN+1/plan.md`. Don't grow the active plan without limit.

## Conventions

- Must run on Windows, macOS, and Linux. Avoid CGO where possible so builds cross-compile. Never assume CUDA or a particular GPU. Use `filepath`, not hard-coded separators.
- Keep everything root-relative. Per-folder state lives in `.ragalay/`. Scan folders must be inside the root and are stored as relative paths. Large shared downloads (Python venv, llama.cpp, model weights) are the one exception: they go in the user cache dir.
- **Search must never start Python.** Python is only for indexing.
- Only one process indexes at a time (`.ragalay/index.lock`).
- No telemetry. The only network calls are setup downloads, the opt-out update check, and the embedding service a folder is configured to use. Sending documents to a non-loopback service needs one-time consent per host per folder (`--yes`). API keys come only from the environment variable the config names, never from files in the folder.
- The README is written for non-technical users first, with the agent and power-user material at the end.
- The embedding model is chosen by profile (`[embed] profile`). Store the space (`embed_id`) and dimension with every embedding; the live settings are recorded in `meta.embed_config`. Jina's `embed_id` must stay byte-identical to Plan 1's.
- CLI output that agents read must offer `--json`.
