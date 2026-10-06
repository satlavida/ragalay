# Plan 1: ragalay core (MD, PDF, images → hybrid search via CLI, MCP, TUI)

**Status:** Finalized (Phase 0 not started)
**Created:** 2026-10-06 · **Finalized:** 2026-10-06 (after 4 grilling rounds)
**Follow-ups:** `plans/plan2` (audio/video, search by example), `plans/plan3` (speed, background indexing, distribution)

---

## 1. What we're building

`ragalay` is one binary you drop into any folder. It indexes the Markdown, PDF, and image files in the folders you choose. AI agents search them through a CLI (`--json`) and an MCP server, and people search them through a terminal UI. Double-clicking the binary opens the UI, and a setup wizard does everything else. It runs on Windows, macOS, and Linux.

## 2. Decisions

### 2.1 Core
| # | Decision |
|---|---|
| D1 | Indexing model: **`jinaai/jina-embeddings-v5-omni-small-retrieval`** (1024-dim, Matryoshka, text + image + PDF page). Runs in a **Python sidecar used only for indexing**. |
| D2 | Query model: **`jinaai/jina-embeddings-v5-text-small-retrieval` GGUF via llama.cpp**. Jina documents its text vectors as identical to omni's, so search never starts Python. If llama.cpp is unavailable, search falls back to keyword-only. |
| D3 | Database: **Turso's CGO-free Go driver** (`tursogo`, purego), with an embedded file `.ragalay/index.db`. It stores the embeddings and the keyword (BM25) index. |
| D4 | Scanning: on-demand `scan` plus `scan --watch`. The TUI watches while open. |
| D5 | In scope: MCP server, hybrid search (vector + BM25, RRF). **Out:** audio and video (→ Plan 2), OCR (→ Plan 3). |
| D6 | Platforms: windows/amd64, darwin/arm64, darwin/amd64, linux/amd64, linux/arm64. No CGO. Never assume CUDA. |
| D7 | Repo `github.com/satlavida/ragalay`. Module path renamed to that in Phase 1. Code license **Apache-2.0**. |

### 2.2 From grilling (2026-10-06)
| # | Decision |
|---|---|
| G1 | Ingest is a per-file Go job queue (PDFs report per-page progress). The UI shows an ETA. No fixed scale target. Brute-force vector search is fine, with an ANN index if `tursogo` supports one. |
| G2 | Python only for indexing (D1/D2). Phase 0 checks GGUF ↔ omni query-vector parity, using F16 if Q8 drifts. |
| G3 | **Query-vector cache** table keyed by (embed_id, normalized query) storing vector, `hit_count`, `last_used`. LFU + TTL eviction (~10k entries). Cleared on model change. Results are never cached. |
| G4 | llama.cpp integration picked in Phase 0: in-process purego (`yzma`) preferred, `llama-server` subprocess as fallback. |
| G5 | Scan folders **must be inside the root**. Paths are stored relative to it, so moving or copying the whole folder keeps the index valid. |
| G6 | MD-linked images: follow the link as written (relative to the MD), plus an `assets/` convention. The image must resolve inside the root, otherwise ragalay warns. |
| G7 | MD ↔ PDF live in separate folders. Pairing order: front-matter `source:` first, then mirrored-folder config (`[[pairs]] md = "md/", pdf = "pdf/"`), then the same base name (left unpaired with a warning if ambiguous). |
| G8 | Personal/research use, to be open-sourced. The README says clearly the default models are **CC BY-NC 4.0**. License acceptance is asked once and recorded. |
| G9 | Setup downloads the `uv` binary from GitHub releases (checksum verified, no remote scripts). It runs on first use after one prompt showing download sizes. The PyTorch wheel is picked per machine: CUDA / ROCm / MPS / CPU. |
| G10 | Moves and renames are matched by sha256, so embeddings are kept and only the path changes. |
| G11 | Test hardware: Windows + AMD RX 9070 XT, Mac M3 Pro, Linux via CI. |
| G12 | **Single indexer** via `.ragalay/index.lock` (pid, progress). Other processes report "indexing in progress (ETA)". Searches are read-only. |
| G13 | Double-click (no args) opens the TUI. First run is a **setup wizard**: choose folders, accept the license, download with progress, first scan. |
| G14 | Unsigned binaries. The README shows "Open anyway" steps with screenshots. Signing, Homebrew, and winget come in Plan 3. |
| G15 | Search output: one result per chunk by default with full chunk text (≤ ~2k chars, `--max-chars`), `k=10`, and `--group-by doc`. |
| G16 | Chunking: 512 tokens with 64 overlap, split at headings first. Tuned on real documents in Phase 0. |
| G17 | The Turso native library is **embedded in the binary** and extracted on first run. llama.cpp, the GGUF, the omni weights, and the Python venv are **downloaded during setup** into a shared user cache. |
| G18 | Indexing runs only while the TUI, `scan`, `scan --watch`, or MCP is running. It resumes from the queue on the next launch. |
| G19 | The TUI checks GitHub releases for updates at most once a day (can be disabled). `ragalay update` swaps the binary. **No telemetry.** |

## 3. Background facts

- **omni-small:** ~1.56B params (frozen Qwen3-0.6B text tower + vision/audio encoders). 32k context, last-token pooling. Needs `transformers>=4.57` (≥5.1 recommended), `torch>=2.5`, `trust_remote_code=True`. Query and document sides are asymmetric: `encode_query`/`encode_document`, or the prefixes `Query: ` / `Document: `.
- **text-small GGUF:** 677M params. Q8_0 is 639 MB, F16 is 1.2 GB. Run with `--pooling last`. ragalay adds the `Query: ` prefix itself.
- **Turso vectors:** `F32_BLOB(1024)`, `vector_distance_cos`, optional `libsql_vector_idx` + `vector_top_k`. Phase 0 confirms what `tursogo` actually supports, including FTS. If FTS is missing, `bleve` is the BM25 fallback.
- **Python:** a `uv`-managed **Python 3.11** venv in the shared cache. The system Python is never used.
- **Devices:**
  - Indexing: CUDA, then ROCm (Windows RX 9070 XT is a spike), then MPS (M3 Pro), then CPU.
  - Queries: llama.cpp Metal on Mac, Vulkan on Windows/Linux (covers AMD), CPU otherwise.

## 4. Design

### 4.1 Files on disk
```
<root>/                          # wherever the binary was dropped
  ragalay(.exe)
  <user folders>/...             # only folders inside root can be scanned (G5)
  .ragalay/
    config.toml                  # folders, pairs, chunking, dim, update check, ignore globs
    index.db                     # documents, chunks, vectors, FTS, query cache
    index.lock                   # present only while indexing (G12)
    lib/                         # extracted Turso native lib (G17)
    logs/

<os.UserCacheDir()>/ragalay/     # shared by every root, relocatable via config
  bin/uv(.exe)
  py/                            # Python 3.11 venv + sidecar script
  llama.cpp/<ver>-<backend>/
  models/                        # GGUF + HF cache for omni weights
```

### 4.2 config.toml (defaults)
```toml
[scan]
folders = ["."]
ignore  = ["**/node_modules/**", "**/.git/**"]
kinds   = ["md", "pdf", "image"]

[[pairs]]                        # optional, G7
md  = "md/"
pdf = "pdf/"

[chunk]
tokens = 512
overlap = 64

[embed]
dim = 1024                       # Matryoshka: 1024/768/512/256/128/64/32
index_model = "jinaai/jina-embeddings-v5-omni-small-retrieval"
query_model = "jinaai/jina-embeddings-v5-text-small-retrieval-GGUF:F16"   # or Q8_0 after the Phase 0 parity check

[cache]
query_max = 10000
query_ttl = "720h"

[update]
check = true
```

### 4.3 Schema
```sql
meta(key PRIMARY KEY, value)            -- schema_version, embed_id, embed_dim, license_accepted_at
documents(id, path UNIQUE, kind, sha256, size, mtime, status, error,
          chunk_count, pair_document_id, embed_id, added_at, processed_at)
  -- kind: markdown | pdf | image ; status: pending | processing | done | failed | stale
doc_links(parent_id, child_id, heading_path, alt_text)   -- MD → linked image (G6), many-to-many
chunks(id, document_id, ord, modality, text, heading_path, page, token_count,
       embedding F32_BLOB(<dim>))
  -- modality: text | pdf_page | image
query_cache(embed_id, query_norm, vector, hit_count, last_used, created_at,
            PRIMARY KEY(embed_id, query_norm))
jobs(document_id PRIMARY KEY, state, attempts, last_error, enqueued_at)   -- G1/G18 resumable queue
-- + vector index (if supported) and FTS over chunks.text
```

### 4.4 Indexing (Python omni sidecar)
1. **Scan** the configured folders. Compare `(size, mtime)`, then `sha256`. A path that disappears while the same hash appears elsewhere counts as a **move** (G10). New or changed files are enqueued.
2. **Lock:** take `index.lock`. If another live process holds it, report its progress and exit or wait.
3. **Per-file jobs** (text first, then PDFs, then images):
   | Kind | Extraction | Chunks |
   |---|---|---|
   | Markdown | Go `goldmark`: headings, front matter (`source:`), image links | text chunks (512/64, heading-prefixed). Linked images → `doc_links` + one `image` chunk each (alt text + heading as metadata, or combined input if Phase 0 shows omni supports it) |
   | PDF | Go per-page text (lib chosen in Phase 0). Sidecar renders pages (`pypdfium2`) | text chunks per page (skipped when a paired MD exists) + **one `pdf_page` image chunk per page** (covers scanned pages) |
   | Image | sidecar | one `image` chunk. An image already linked from an MD isn't indexed twice |
4. **Embed** in batches with `encode_document`. Vectors are L2-normalized, truncated to `dim` if needed, and renormalized.
5. **Store** one transaction per document and mark it `done`. Failures are marked `failed` with a reason (e.g. "no text and no renderable pages") and retried up to N times on the next scan. ETA comes from the average time per page or file.
6. The sidecar is a long-lived child process speaking JSON-RPC over stdio, embedded with `go:embed`. It starts only when there is indexing work and exits when the queue is empty.

### 4.5 Search (no Python)
1. Normalize the query (trim, collapse whitespace, lowercase for the cache key). Look up `query_cache`. On a miss, embed `Query: <q>` with llama.cpp (text-small GGUF), store it, and bump the hit count.
2. Vector search (cosine, ANN if available) and BM25 over the text chunks, merged with **RRF (k=60)**. `--mode hybrid|vector|keyword`. If llama.cpp is unavailable, it falls back to keyword-only with a notice.
3. Filters: `--modality`, `--path-glob`. **Pair dedup:** an MD hit and a PDF hit on the same page merge into one result.
4. Output: one result per chunk (default) or `--group-by doc`. `--max-chars` (default 2000). Stable JSON:
   `[{score, path, kind, modality, page, heading_path, text, paired_path, parent_path}]`.

### 4.6 Model swap
`embed_id` = provider + model + precision + dim, recorded in `meta` and on each document. A mismatch prints a warning (what changed, how many documents, ETA), exits with code 2, and the TUI shows a banner until `ragalay reembed` runs. That command is resumable and clears `query_cache`.

### 4.7 CLI
```
ragalay                      # TUI (setup wizard on first run)
ragalay init | setup
ragalay folders add|remove|list [--json]
ragalay scan [--watch]
ragalay status [--json] | docs [--status ..] [--kind ..] [--json]
ragalay search "q" [-k 10] [--mode ..] [--modality ..] [--path-glob ..]
                   [--group-by doc] [--max-chars N] [--json]
ragalay reembed | update | mcp | version
```
Exit codes: 0 ok, 1 error, 2 model mismatch, 3 not initialised, 4 setup incomplete, 5 indexing locked (when `--no-wait`).

### 4.8 MCP
Official Go SDK, stdio. Tools: `search`, `status`, `list_documents`, `list_folders`, `scan`. The README has copy-paste configs for Claude Code / Claude Desktop.

### 4.9 TUI (Bubble Tea)
- **Setup wizard** (first run): welcome, choose folders (inside root), license (CC BY-NC), download sizes, then downloads with progress bars, device detected, first scan.
- **Search:** box, modality filter, results, preview. Enter opens the file (PDF at the page where the viewer supports it). `o` opens a linked image.
- **Status:** counts by kind and status, live queue with ETA, failed list.
- **Folders:** list, `a` add, `d` remove.
- Update notice (G19). Model-mismatch banner.

## 5. Phases

### Phase 0: Spikes and decisions
- [ ] `tursogo` on Windows, macOS (M3 Pro), Linux: `F32_BLOB(1024)`, `vector_distance_cos`, ANN/`vector_top_k`, FTS. One process writing while another reads (G12)
- [ ] Embed the Turso native lib in the binary and extract it on first run, on 2 OSes (G17)
- [ ] omni sidecar: uv + Python 3.11, embed text, image, and PDF page over stdio. Measure throughput and RAM on Windows CPU, **ROCm-on-Windows (RX 9070 XT)**, and **MPS (M3 Pro)**
- [ ] Check whether omni accepts a combined image + text input
- [ ] llama.cpp query embedding: `yzma` in-process vs `llama-server`, on Windows Vulkan and Mac Metal. Pick one (G4)
- [ ] Parity: cosine(omni text vector, GGUF Q8_0 / F16 vector) on ~50 queries. Pick the query GGUF precision
- [ ] Pure-Go PDF text extraction: compare libraries on 3–5 real PDFs
- [ ] Chunk size: retrieval check at 512/64 against alternatives on real documents (G16)
- [ ] Record the results in §3 and adjust defaults
- **Exit:** every bullet has a working proof, and the choices are recorded in this plan.

### Phase 1: Skeleton, config, store
- [ ] Rename module to `github.com/satlavida/ragalay`. Add Apache-2.0 `LICENSE`
- [ ] Package layout `internal/{config,store,scan,extract,chunk,embed,sidecar,llama,search,mcp,tui,update}`
- [ ] Cobra CLI, root discovery, `init`, `config.toml` defaults and validation
- [ ] `folders add/remove/list`, rejecting paths outside the root (G5)
- [ ] Schema plus migrations (§4.3), `status --json`
- **Exit:** `ragalay init && ragalay folders add docs && ragalay status --json` works on Windows, and CI builds all 5 targets.

### Phase 2: Runtimes and setup
- [ ] Shared cache, downloader (progress, checksum, resume), `uv` fetch (G9)
- [ ] venv plus per-device wheel selection (CUDA/ROCm/MPS/CPU), embedded sidecar script, JSON-RPC client, lifecycle
- [ ] llama.cpp fetch (backend per OS/GPU), GGUF download, query embedder (G2/G4)
- [ ] `query_cache` with LFU/TTL eviction (G3)
- [ ] `setup` command: license record, self-test for both runtimes
- **Exit:** `ragalay setup` works from scratch on Windows (AMD) and the M3 Pro, and a Go test embeds through both runtimes.

### Phase 3: Scan, records, pairing, lock
- [ ] Walker over the configured folders, ignore globs, kind mapping
- [ ] `(size, mtime)` → `sha256` change detection, deletions, **move detection** (G10)
- [ ] `jobs` queue, crash recovery, `index.lock` (G12)
- [ ] Pairing: front matter, then `[[pairs]]`, then base name (G7)
- [ ] `scan --watch` with fsnotify and debouncing, following folder changes
- **Exit:** add/edit/move/delete/pair in test folders shows correctly in `status --json`. A second indexer is refused cleanly.

### Phase 4: Extraction and chunking
- [ ] MD: goldmark AST, heading paths, front matter, image links resolved per G6, `doc_links`
- [ ] PDF: per-page text plus page-image inputs, skipping text for paired PDFs
- [ ] Standalone images, deduplicated against linked images
- [ ] Recursive chunker (512/64, heading-first) with golden tests
- **Exit:** golden tests pass for MD + linked images, text PDF, scanned PDF, and standalone images.

### Phase 5: Ingest and model swap
- [ ] Job runner: priority order, batching, one transaction per document, retries, ETA, resume after quit (G18)
- [ ] Populate the vector and FTS indexes
- [ ] `embed_id` mismatch → exit 2 / banner, resumable `reembed` that clears the cache
- **Exit:** a mixed sample folder indexes end to end, survives being killed mid-run, and a change to `dim` triggers a re-embed.

### Phase 6: Search and MCP
- [ ] Vector + BM25 + RRF, modes, filters, pair dedup, `--group-by doc`, `--max-chars`, stable JSON
- [ ] Keyword-only fallback when llama.cpp is missing
- [ ] `ragalay mcp` with 5 tools, tested from Claude Code
- **Exit:** an agent gets cited results (path + page) through `search --json` and MCP, and search never starts Python (checked in a test).

### Phase 7: TUI
- [ ] First-run setup wizard (G13)
- [ ] Search, preview, open file or page, `o` for linked images
- [ ] Status (queue, ETA, failed) and Folders views
- [ ] Update notice and mismatch banner
- **Exit:** a non-technical tester double-clicks the binary in a fresh folder and reaches working search without typing a command.

### Phase 8: Release and docs
- [ ] goreleaser for 5 targets, GitHub Actions matrix (windows, macos, ubuntu) running tests
- [ ] `ragalay update` (G19)
- [ ] **README for non-technical users:** download, drop into a folder, double-click, "Open anyway" screenshots (G14), choosing folders, keeping MD images next to the MD, model license note, no-telemetry statement. "For AI agents" section at the end (`--json`, MCP config)
- [ ] Drop-in test on Windows (AMD) and the M3 Pro
- **Exit:** a tagged release publishes binaries, and the drop-in test passes on both machines.
