# Plan 6: better progress and time estimates

**Status:** Completed
**Created:** 2026-10-13 · **Finalized:** 2026-10-13 (user request: "better timing estimates and better % remaining indicators"; layout chosen by the user)
**Relates to:** Plan 3 (media needs duration-based estimates; it plugs into the cost model built here)

## 1. Problem

Today's ETA (`internal/index/index.go`, `eta`) is a per-document-kind average of wall seconds, with fixed guesses for a first run (md 1 s, pdf 10 s, image 1 s). It ignores file size and PDF page count, so one 400-page PDF and a 2-page PDF count the same. The first embedding model load is counted as document time. Progress shows `done/total` documents, which stalls on big files and says nothing about percent of work.

## 2. Decisions

| # | Decision |
|---|---|
| D1 | Progress is measured in **work units**, not documents. Each queued job gets a cost estimate in units (PDF: pages; Markdown/code: bytes or chunk estimate; image: 1; Plan 3 media: seconds of duration). Percent = completed units / total units. |
| D2 | Time per unit is learned **per kind** and persisted in `meta` (`eta_rate_<kind>`), so run 2 starts informed. The old `eta_<kind>` keys are ignored. |
| D3 | One-off costs (model load, first embed warm-up) are measured separately and excluded from per-unit rates. |
| D4 | Rates use an exponentially weighted average so a changed device or model adapts within a few documents. |
| D5 | The estimate is shown as a **range-aware, rounded** value ("about 12 min left"), and a kind needs 3 timed documents (or a saved rate) before it counts as measured; until then the display says "estimating". |
| D6 | Progress inside a document (PDF pages, chunk batches) is reported, so the bar moves during a long file. |
| D7 | The CLI and the TUI read the same `index.Progress` struct, extended with `Percent` and `Estimating`. `index --json` prints only the final summary (no progress events), so it is unchanged. |

## 3. Phases

### Phase 1: Cost model and persisted rates ✅ Completed (2026-10-13)
**Goal:** Every job has a unit cost; the ETA uses learned seconds per unit.
- [x] Add `internal/index/progress.go`: `cost(job)` (file size per unit: 4 KiB of text, ~80 KiB per PDF page, 1 per image; real page counts are a later refinement), `rates` with EWMA load/save under `eta_rate_<kind>`.
- [x] Replace `eta` in `index.go`; keep model start time out of rates.
- [x] Unit tests: cost per kind, EWMA convergence, persistence round-trip, first-run "estimating" state.

**Exit:** `go test ./internal/index` passes; a mixed folder of small and large files gives an ETA proportional to size.

### Phase 2: In-document progress ✅ Completed (2026-10-13)
**Goal:** Percent moves during one long document.
- [x] Extend `Progress` with `UnitsDone`, `UnitsTotal`, `Percent`, `Estimating`.
- [x] Report between extraction pages and between embedding batches.
- [x] Tests with a fake embedder that checks monotonic, bounded percent ending at 100.

**Exit:** percent never goes backwards or above 100, including after failed and linked documents.

### Phase 3: CLI and TUI display ✅ Completed (2026-10-13)
**Goal:** People and agents see the new numbers.
- [x] CLI line: `Indexing 37% (12/32 files), about 8 min left: path`; non-TTY lines the same, throttled.
- [x] TUI activity line and detail view: a bar plus percent and ETA.
- [x] README note on what the estimate means.

**Exit:** `go test ./...` passes (`-short`; the real-model tests were not run). Not yet checked on a real folder with a model installed.
