# Plan 1: ragalay core (MD, PDF, images → hybrid search via CLI, MCP, TUI)

**Status:** In progress (Phases 0–6 completed 2026-10-06; next: Phase 7)
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
| G4 | llama.cpp integration: **yzma in-process, CPU backend, text-small Q8_0** (decided in Phase 0, §3.1). |
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
| G16 | Chunking: **256 tokens with 32 overlap** (Phase 0 eval, §3.1; was 512/64), split at headings first. Configurable. Re-check on real documents. |
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

### 3.1 Phase 0 results (Windows 11, Ryzen 5 5600X, RX 9070 XT; experiment code in `spikes/`)
**tursogo v0.8.1** (`spikes/turso`, `spikes/tursolock`)
- ✅ `F32_BLOB(1024)`, `vector32()`, raw little-endian f32 blobs, `vector_distance_cos`, `vector_extract`.
- ✅ Native library ships via `go:embed` inside `turso-go-platform-libs` (~16 MB on Windows) and is extracted by hash to `os.UserCacheDir()` (override: `TURSO_GO_CACHE_DIR`). **G17 needs no work from us.** Embedded for windows/amd64, darwin/arm64+amd64, linux/amd64+arm64 (glibc + musl).
- ❌ **No dense ANN index.** `libsql_vector_idx` isn't supported. The only index method (`experimental=index_method`) is `toy_vector_sparse_ivf`, which needs sparse vectors. **Decision: brute-force `ORDER BY vector_distance_cos`**, measured at 70 ms per 10k × 1024-dim (≈0.7 s per 100k). Possible later optimization: Matryoshka 256-dim prefilter plus a 1024-dim rerank.
- ❌ **No FTS.** No `fts5` module, and the `fts` index method isn't in this build. **Decision: BM25 tables we maintain ourselves in Turso** (`terms`, `postings`, `chunks.len`), scored in Go (`spikes/bm25`). 20k chunks: queries take 6–110 ms, indexing ~1.6 ms per chunk. Everything stays in one transactional file, matching "ragalay stores the BM25 data".
- ❌ **Exclusive cross-process file lock on Windows.** A second process can't even read while another has the DB open. `experimental=multiprocess_wal` gives "not supported by the active IO backend" on Windows.
  - ✅ Inside one process, a pool of 4 readers plus a writer works with no errors.
  - ✅ Open → query → close takes **~9 ms**.
  - **Decision (implements G12):** short-lived connections. The indexer opens the DB only for each document's write transaction (embedding runs with the DB closed). Search, status, and other processes open, read, and close, with retry and backoff on `Locking error` (up to ~5 s, then "indexing in progress, retry"). Long-running processes (TUI, MCP, `scan --watch`) don't hold the DB open while idle.

**PDF text** (`spikes/pdftext`): **go-pdfium in WebAssembly mode (wazero, no CGO)** chosen.
- `ledongthuc/pdf` drops all spaces between words ("AttentionIsAllYouNeed"), which rules it out.
- pdfium gives correct spacing and ligatures at ~6–12 ms per page, after a one-time ~2.5 s wasm startup.
- Scanned pages return 0 chars, so detection is trivial.

**llama.cpp** (`spikes/llama`): `yzma install -p vulkan` fetches llama.cpp v0.5.0 for Windows Vulkan (30 MB download, 88 MB unpacked, sha256-pinned). The same bundle includes `llama-server.exe`, so either integration mode needs only one download.
- Text-small Q8_0, 50 queries with the `Query: ` prefix:

  | Mode | Startup | First query | Steady state |
  |---|---|---|---|
  | yzma, CPU | 0.46 s | 0.24 s | 113 ms |
  | yzma, Vulkan | 1.3 s | 2.9 s (shader warmup) | 140 ms |
  | llama-server, Vulkan | 3.8 s | 0.96 s | 95 ms |

- **Decision (G4): yzma in-process, CPU backend, for query embedding.** A cold one-shot search takes ~0.7 s to get its vector, and cache hits are instant. The GPU doesn't help short queries and adds driver risk. So search uses the **CPU llama.cpp build on every OS** (smaller download, no Vulkan/Metal dependency). GPU stays an option for indexing text in Plan 3.
- Windows gotcha: put the llama.cpp lib dir on `PATH` before `llama.Load`, otherwise `ggml.dll` can't find its sibling DLLs.
- **Parity with omni query vectors** (50 queries):
  - Q8_0: cosine min 0.9996 / median 0.9997, **top-1 doc identical 50/50**.
  - F16: median 0.9999, also 50/50.
  - Without the `Query: ` prefix: median 0.92, only 36/50. **The prefix is mandatory.**
  - **Decision: Q8_0** (639 MB). F16 brings no ranking gain for twice the size.

**omni-small** (`spikes/omni`, `spikes/chunkeval`), sentence-transformers 6.1, transformers 5.18, torch 2.14:
- **Pin the model revision** (`e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4`). The model runs remote code, and that code changed while the model was downloading. Weights are ~4 GB (bf16). Prompts are `Query: ` / `Document: `, dim 1024.
- **The custom code ignores the `dtype` load argument (it forces bf16).** Cast after loading with `model.to(dtype)`.
  - CPU: bf16 is ~7x slower than fp32 on Zen 3, so **CPU uses fp32**.
  - GPU: **bf16**.
- **Requires `torchvision`.** Without it, the image processor is `None` and image embedding crashes.
- Throughput:

  | Device | RAM / VRAM | Text chunk (~400 tok) | Image / PDF page |
  |---|---|---|---|
  | CPU fp32 (Ryzen 5600X) | 6.9 GB | 1.4 s | 4.1 s @448 px, 6.6 s @612x792 |
  | **ROCm, RX 9070 XT, bf16** | 1.7 GB host | **0.06 s** (~0.025 s batched) | **0.10-0.16 s** up to 1024 px, after warmup |

- **ROCm on Windows works for the RX 9070 XT.** Install from AMD's index: `--index-url https://stable.repo.amd.com/rocm/whl-next/ "torch[device-gfx1201]==2.14.0+rocm10.1.0" "torchvision[device-gfx1201]==0.29.0a0+rocm10.1.0"`. `torch.cuda.is_available()` returns True.
  - **Gotcha:** MIOpen compiles kernels for every new image shape (4-12 s the first time).
  - **Decision:** resize each image or page to a fixed bucket (long side 1024, padded to a few fixed shapes) so compiles happen once.
- **Combined text + image input works:** `encode_document([(text, image)])`. Cosine against the image alone is 0.993. **Decision: MD-linked images are embedded with their alt text + heading as one combined input** (G6).
- Scanned page image vs the original page image: cosine 0.995, so page images cover scanned PDFs.
- **Chunk-size eval** (20 labelled queries over the Attention + BERT papers, page-level scoring):

  | Unit | hit@1 | hit@5 | MRR |
  |---|---|---|---|
  | **256/32 tokens** | **0.75** | **0.90** | **0.822** |
  | 512/64 | 0.70 | 0.85 | 0.775 |
  | 1024/128 | 0.45 | 0.85 | 0.600 |
  | whole page | 0.50 | 0.90 | 0.644 |
  | page image 1024 px | 0.65 | 0.90 | 0.774 |

  - **Decision: default 256/32.** Small sample, so revisit with real user documents.
  - Page images alone almost match 512-token text, which supports the PDF page-image design.
- **CPU-only cost:** a 100-page PDF costs roughly 10 min for page images plus 3 min for text. The ETA must make that visible (G1).

**Deferred:** macOS (M3 Pro: MPS, Metal, tursogo dylib), user 2026-10-06. Linux gets checked by CI in Phase 1.

## 4. Design

### 4.1 Files on disk
```
<root>/                          # wherever the binary was dropped
  ragalay(.exe)
  <user folders>/...             # only folders inside root can be scanned (G5)
  .ragalay/
    config.toml                  # folders, pairs, chunking, dim, update check, ignore globs
    index.db                     # documents, chunks, vectors, BM25 tables, query cache
    index.lock                   # present only while indexing (G12)
    logs/                        # setup.log, sidecar.log

<os.UserCacheDir()>/ragalay/     # shared by every root; override with RAGALAY_CACHE
  state.json                     # what setup installed + license acceptance (machine-wide)
  bin/uv-<ver>/uv(.exe)
  python/                        # uv-managed Python 3.11 interpreters
  py/<variant>/                  # venv per torch variant (cpu, cuda, mps, rocm-gfx1201, ...)
  py/sidecar.py                  # written from the binary (go:embed)
  llama.cpp/<ver>-cpu/
  models/v5-small-retrieval-Q8_0.gguf
  uv-cache/                      # unless UV_CACHE_DIR is already set
<HF cache>                       # omni weights: Hugging Face's standard cache (honours HF_HOME), shared with other tools
```
The Turso native lib is extracted by tursogo itself to `os.UserCacheDir()/<hash>/` (§3.1), not to `.ragalay/lib/`.

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
tokens = 256
overlap = 32

[embed]
dim = 1024                       # Matryoshka: 1024/768/512/256/128/64/32
index_model = "jinaai/jina-embeddings-v5-omni-small-retrieval"
index_revision = "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4"   # pinned remote code
query_model = "jinaai/jina-embeddings-v5-text-small-retrieval-GGUF:Q8_0"
image_max_side = 1024            # fixed buckets avoid per-shape kernel compiles on ROCm

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
terms(id, term UNIQUE, df)                                -- BM25 (Turso has no FTS, §3.1)
postings(term_id, chunk_id, tf, PRIMARY KEY(term_id, chunk_id))
query_cache(embed_id, query_norm, vector, hit_count, last_used, created_at,
            PRIMARY KEY(embed_id, query_norm))
jobs(document_id PRIMARY KEY, state, attempts, last_error, enqueued_at)   -- G1/G18 resumable queue
-- no ANN index: brute-force vector_distance_cos (§3.1). chunks.token_count doubles as the BM25 doc length
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
1. Normalize the query (trim, collapse whitespace; **case is kept**, because the embedding is case-sensitive for acronyms and names; Phase 2). The cache key also includes the query model ID. Look up `query_cache`. On a miss, embed `Query: <q>` with llama.cpp (text-small GGUF), store it, and bump the hit count.
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

### Phase 0: Spikes and decisions ✅ Completed (2026-10-06)
- [x] `tursogo` on Windows (+ Linux via CI; **macOS deferred**, user 2026-10-06): `F32_BLOB(1024)`, `vector_distance_cos`, ANN/`vector_top_k`, FTS. One process writing while another reads (G12). Vectors work. No ANN, no FTS, exclusive lock (see §3.1)
- [x] Embed the Turso native lib in the binary and extract it on first run (G17): built into tursogo. Verified on Windows; other OSes via CI/deferred
- [x] omni sidecar: uv + Python 3.11, embed text, image, and PDF page over stdio. Measure throughput and RAM on Windows CPU and **ROCm-on-Windows (RX 9070 XT)**. MPS (M3 Pro) deferred
- [x] Check whether omni accepts a combined image + text input (yes)
- [x] llama.cpp query embedding: `yzma` in-process vs `llama-server`, on Windows Vulkan (Mac Metal deferred). Pick one (G4)
- [x] Parity: cosine(omni text vector, GGUF Q8_0 / F16 vector) on ~50 queries. Pick the query GGUF precision
- [x] Pure-Go PDF text extraction: compare libraries on real PDFs (2 papers + a scanned PDF; go-pdfium wasm)
- [x] Chunk size: retrieval check at 512/64 against alternatives (G16): 256/32 wins
- [x] Record the results in §3 and adjust defaults
- **Exit:** every bullet has a working proof on Windows, and the choices are recorded in this plan. **Mac checks are deferred** (user, 2026-10-06) and get re-run before the Phase 8 drop-in test.

### Phase 1: Skeleton, config, store ✅ Completed (2026-10-06)
- [x] Rename module to `github.com/satlavida/ragalay`. Add Apache-2.0 `LICENSE`. Spikes moved to their own module (`spikes/go.mod`) so their deps stay out of the product
- [x] Package layout `internal/{config,store,scan,extract,chunk,embed,sidecar,llama,search,mcp,tui,update}`: `config`, `store`, `cli` exist now. The others are created in the phase that fills them (no empty stubs)
- [x] Cobra CLI, root discovery (`--root` / `RAGALAY_ROOT`, then walk up from cwd, then from the binary's folder), `init`, `config.toml` defaults and validation (unknown keys rejected, all errors reported at once)
- [x] `folders add/remove/list`, rejecting paths outside the root (G5). Empty list = whole directory. Overlaps are normalized, and removal marks documents `stale` unless `--keep`
- [x] Schema plus migrations (§4.3), `status --json`. `store.With` = short-lived connection + lock-retry, tested against a real second process
- **Exit:** `ragalay init && ragalay folders add docs && ragalay status --json` works on an empty folder on Windows, and CI builds all 5 targets.
  - ✅ Verified on Windows: by hand in a fresh folder, and in `internal/cli` tests.
  - ✅ All 5 targets cross-compile with `CGO_ENABLED=0`. `.github/workflows/ci.yml` (test on windows/macos/ubuntu + cross-build) is written but **hasn't run yet**: no GitHub remote. It runs on the first push.
  - Found: Turso doesn't enforce the `F32_BLOB(N)` dimension, so writers must check vector length (Phase 5).

### Phase 2: Runtimes and setup ✅ Completed (2026-10-06)
- [x] Shared cache, downloader (progress, checksum, resume), `uv` fetch (G9): `internal/download` (resume via Range, sha256, zip/tar.gz with traversal protection). uv 0.12.23 pinned per platform
- [x] venv plus per-device wheel selection (CUDA / ROCm gfx-specific index / MPS / CPU, always with torchvision), pinned model revision, dtype cast (GPU bf16, CPU fp32), fixed image buckets, embedded sidecar script, JSON-RPC client, lifecycle: `internal/setup` + `internal/sidecar`.
  - Detection before install: nvidia-smi → cuda (cu126); Apple Silicon → mps; Windows RX 9070/9060 → rocm-gfx1201/1200; everything else → cpu.
  - After install, setup checks torch really sees the GPU and falls back to CPU if not.
  - Images go into 3 fixed buckets (portrait/square/landscape, 28-px aligned).
- [x] llama.cpp **CPU** build fetch, Q8_0 GGUF download, yzma query embedder with `Query: ` prefix and PATH fix on Windows (G2/G4): `internal/llama`.
  - **Not yzma's installer:** it pulls in hashicorp/go-getter and cloud SDKs. Instead, llama.cpp b11146 asset URLs and sha256s (from yzma's manifest) are pinned in `internal/setup/pins.go` and fetched with our downloader.
- [x] `query_cache` with LFU/TTL eviction (G3): `store.CachedQuery/PutQuery/EvictQueries/ClearQueries`
- [x] `setup` command: license record, self-test for both runtimes.
  - License acceptance is recorded once per machine (`state.json`) and in each folder's `meta`.
  - Download sizes are shown before anything downloads. Without a terminal, setup refuses unless `--accept-license --yes` is given (exit 4). EOF never counts as consent.
  - `status` shows setup readiness and the device.
- **Exit:** `ragalay setup` works from scratch on Windows (AMD) and the M3 Pro, and a Go test embeds through both runtimes.
  - ✅ Windows + RX 9070 XT: setup completed (ROCm torch, self-test parity **0.9997**).
  - ✅ Windows CPU-only (`--device cpu`, separate cache): setup completed. Indexing model loads in fp32 in 20.8 s, a text + image take 12.5 s, the search model loads and embeds in 0.58 s, parity **0.9998**.
  - Found during that run: Hugging Face dropped the connection mid-download. The downloader now **retries network errors 5 times with backoff, resuming each time** (HTTP 4xx isn't retried), and the re-run resumed from the partial file.
  - ✅ `TestBothRuntimesOnThisMachine`: text, image, captioned image, and PDF page through the GPU sidecar, plus a llama.cpp query. Attention text 0.853 vs bread 0.045, PDF page 0.568. 256-dim truncation keeps the ranking.
  - ⏸ M3 Pro deferred (user, 2026-10-06).
  - Untested variants: cuda, mps, rocm-gfx1200.

### Phase 3: Scan, records, pairing, lock ✅ Completed (2026-10-06)
- [x] Walker over the configured folders, ignore globs, kind mapping (`internal/scan/walk.go`).
  - `**` globs.
  - Hidden files and folders are always skipped.
  - Extensions: md/markdown, pdf, png/jpg/jpeg/webp/gif/bmp.
- [x] `(size, mtime)` → `sha256` change detection, deletions, **move detection** (G10).
  - Only files with a new size or mtime get hashed.
  - A moved file keeps its document id, chunks and embeddings.
  - A file whose mtime changed but whose content didn't stays indexed.
- [x] `jobs` queue, crash recovery, `index.lock` (G12), short-lived DB connections with lock-retry backoff (§3.1).
  - `internal/lock`: O_EXCL lock file holding the pid. A lock left by a dead process is taken over (Windows `OpenProcess`, Unix `kill 0`).
  - `store.RecoverInterrupted` resets `processing` → `pending`.
  - Deleting a document also deletes its chunks and postings and fixes BM25 `df`.
- [x] Pairing: front matter, then `[[pairs]]`, then base name (G7).
  - Three passes, so the more specific rule wins regardless of file order.
  - Pairs are stored both ways.
  - Ambiguous names and broken `source:` give warnings.
- [x] `scan --watch` with fsnotify and debouncing, following folder changes.
  - Recursive watching (new folders get added).
  - A 2 s debounce.
  - `config.toml` changes are reloaded (an invalid config keeps the previous settings).
  - A safety rescan every 10 min.
- **Design change: `[scan] keep`.** `folders remove --keep` now moves the folder into an explicit `keep` list (its documents stay searchable, new files aren't picked up). Anything outside `folders` + `keep` is dropped. That includes folders removed by editing `config.toml` by hand, which before was indistinguishable from `--keep`.
- New commands: `scan [--watch] [--json]` (exit 5 when another indexer holds the lock) and `docs [--status] [--kind] [--json]`. `status` shows the queue, pairs and the running indexer. Ctrl+C cancels cleanly.
- `status` now reports setup readiness from the machine-wide state, so a new folder on a set-up computer shows "ready".
- **Exit:** add/edit/move/delete/pair in test folders shows correctly in `status --json`. A second indexer is refused cleanly.
  - ✅ `TestPhase3Flow` (CLI) and `internal/scan` tests.
  - ✅ By hand with the real binary: the watcher picked up dropped files within ~3 s, front matter paired a transcription with its PDF, a second `scan` exited 5 naming the watcher's pid, and a lock left by a killed watcher was taken over.

### Phase 4: Extraction and chunking ✅ Completed (2026-10-06)
- [x] MD: goldmark AST, heading paths, front matter, image links resolved per G6, `doc_links` (`internal/extract/markdown.go`).
  - GFM. Heading paths look like "Title > Section > Sub". `<!-- page N -->` markers set the page.
  - Images come from `![]()`, reference-style links, and HTML `<img>`. Each resolves relative to the MD (or the root for `/…`), with an `assets/` fallback, and must stay inside the root.
  - Remote images are skipped quietly. Missing, outside-root, and unsupported images give warnings.
  - Image syntax is stripped from chunk text, keeping the alt text. Code fences are kept.
  - Linked images become `image` units embedded **with their caption** (heading path + alt text, combined input).
- [x] PDF: per-page text plus page-image inputs, skipping text for paired PDFs (`internal/extract/pdf.go`).
  - go-pdfium wasm, started once per run.
  - New `[index] pdf_page_images` setting (default true) so CPU-only users can skip page images.
  - A PDF with no text and page images off fails with `ErrNoContent`. Encrypted PDFs give a clear error.
- [x] Standalone images, deduplicated against linked images: extraction gives a standalone image one unit. **The dedup itself (skip or remove a standalone image's chunks when an MD links it) lives in the Phase 5 job runner**, because it needs `doc_links` from already-indexed MDs.
- [x] Recursive chunker (256/32 by tokenizer count, heading-first) with golden tests (`internal/chunk`).
  - Splits on paragraph, then sentence, then word, with word-level overlap.
  - Tiny sections merge into the next (never across pages).
  - Tokens are counted by a `Tokenizer` interface. Production uses the real Qwen3 tokenizer via `llama.Embedder.Count`; tests use a deterministic estimate.
- **Exit:** golden tests pass for MD + linked images, text PDF, scanned PDF, and standalone images. ✅ `internal/extract/testdata/*.golden.json` (regenerate with `go test ./internal/extract -update`), plus spot checks. PDFs are generated in the test, so no binary fixtures are committed.

### Phase 5: Ingest and model swap ✅ Completed (2026-10-06)
- [x] Job runner: priority order, batching, one transaction per document, retries, ETA, resume after quit (G18): `internal/index`.
  - Order is Markdown, then PDF, then images. Embedding calls go in batches of 16.
  - The sidecar starts only when a document actually needs embedding, so an empty queue never starts Python.
  - Failures are marked `failed` with a reason and retried once per run, up to 3 attempts.
  - An interrupted document goes back to the queue.
  - ETA uses per-kind moving averages, kept in `meta` across runs.
  - **Linked-image dedup** (deferred from Phase 4): an image an MD links gets 0 chunks of its own ("indexed with the Markdown file that shows it"). If the MD stops linking it, or the MD is deleted, the image is re-queued to be indexed alone.
- [x] Store vectors and populate the BM25 `terms`/`postings` tables in the same transaction.
  - `store.ReplaceChunks` checks vector dimension (Turso doesn't) and keeps `df` exact across replaces.
  - `internal/bm25` holds the shared tokenizer and scoring.
  - Schema v2: `chunks.source_path` (file of a linked image), `chunks.bm25_len`, and `doc_links` keyed by `child_path`.
- [x] `embed_id` mismatch → exit 2 / banner, resumable `reembed` that clears the cache.
  - `embed_id` = model@rev7:dim. A fresh index adopts the config's space.
  - `scan` exits 2 on a mismatch even with nothing queued. `status` shows a banner.
  - `reembed [--force]` recreates `chunks` at the new dim and clears BM25 and the query cache. The new id is recorded first, so an interrupted rebuild continues with `scan`.
- `scan` now indexes after scanning (`--no-index` to skip), and `scan --watch` indexes after each scan. On a set-up machine indexing just runs; otherwise documents stay queued with a clear message. `scan --json` = scan report + `index` summary.
- **Exit:** a mixed sample folder indexes end to end, survives being killed mid-run, and a change to `dim` triggers a re-embed.
  - ✅ Unit tests (`internal/index`, fake embedder): end to end, linked dedup, paired PDF, broken PDF retry limit, cancellation mid-embed, dim change → mismatch → reembed at 512.
  - ✅ Real models on the RX 9070 XT: papers + paired transcription + linked and standalone images + scanned PDF went to 126 chunks in 40 s. `taskkill /F` mid-run left one document `processing`, and the next `scan` recovered it and finished. Changing `dim` to 512 gave the banner and exit 2, and `reembed` rebuilt everything at 512 dims.

### Phase 6: Search and MCP ✅ Completed (2026-10-06)
- [x] Vector + BM25 + RRF, modes, filters, pair dedup, `--group-by doc`, `--max-chars`, stable JSON (`internal/search`).
  - Vector search is brute-force `vector_distance_cos`. BM25 is scored from our own postings. They merge with RRF (k=60).
  - Filters: `--modality` and `--path-glob` (SQL `GLOB`; `*` also matches across folders).
  - Pair merge: same page, or a page-less transcription folds into its PDF's best hit. The result cites the PDF page and shows the transcription's text.
  - Linked-image hits report `path` = the image and `parent_path` = the MD.
  - The query cache is keyed by space + query model; it's evicted on write and checked against the index dimension.
  - Search refuses on a model mismatch (exit 2).
- [x] Keyword-only fallback when llama.cpp is missing (with a notice on stderr / in `notice`).
- [x] `ragalay mcp` with 5 tools (`internal/mcpserver`, official go-sdk v1.8.0, stdio).
  - Tools: search, status, list_documents, list_folders, scan.
  - The query model loads on the first search and stays loaded. Config is re-read per call.
  - Tested over real stdio with the official MCP client (`spikes/mcpclient`), the same protocol Claude Code uses. Not added to the user's Claude Code config automatically; the README has the one-line `claude mcp add` command.
- Fixes found while testing on real PDFs:
  - PDFium's U+0002 line-end hyphenation marker becomes `-`, and other control characters are stripped.
  - A PDF is re-queued when it gains or loses its transcription (its text was otherwise never indexed after the MD went away).
  - `reembed` scans first.
- **Exit:** an agent gets cited results (path + page) through `search --json` and MCP, and search never starts Python (checked in a test).
  - ✅ `internal/search` tests cover modes, filters, citations, pair merge, group-by, trimming, query cache, fallback, and mismatch. `TestSearchNeverStartsPython` checks that `internal/search` doesn't depend on the sidecar.
  - ✅ Real index: CLI search ~1.0 s cold. "How many layers in the encoder stack" finds Attention p.3 (N = 6). MCP search 464 ms first, 78 ms after.

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
