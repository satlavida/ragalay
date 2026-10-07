# Plan 2: swappable embedding models and external (OpenAI-compatible) embedding services

**Status:** In progress (started 2026-10-07 at the user's request while Plan 1 waits on its user-side Phase 8 items). Phase 0 done.
**Created:** 2026-10-07 · **Finalized:** 2026-10-07 (open questions answered; 2 grilling rounds)
**Depends on:** Plan 1 (embed interfaces, `embed_id`, `ragalay reembed`, query cache)
**Renumbering (2026-10-07):** this plan was drafted as plan4 and moved ahead at the user's request. Audio/video is now Plan 3, and speed/distribution is Plan 4.

---

## 1. Goal

Let the user choose the embedding model per folder, from:

1. **Built-in local profiles**, installed and pinned by `ragalay setup`:
   - `embeddinggemma-2`: **the new default** for new folders. Google, Apache 2.0, 740M multimodal, 768-dim.
   - `jina-v5`: today's model. omni-small indexes, the text-small GGUF embeds queries. CC BY-NC 4.0.
2. **An external service** that speaks the OpenAI embeddings API (`POST {base_url}/embeddings`). This covers:
   - local servers: Ollama, LM Studio, llama.cpp `llama-server`, vLLM, TEI, LocalAI
   - online services: OpenAI, Jina API, Voyage, Together, Mistral, and so on
   - **image inputs** where the provider offers them (§4.4)

Switching models re-embeds the index through the existing `ragalay reembed` flow (plan1 §4.6). Hybrid search, `--json`, MCP, and the TUI behave the same with every profile.

## 2. Decisions

| # | Decision |
|---|---|
| P1 | Plan order: this is **Plan 2**, done right after Plan 1 is archived. |
| P2 | Built-in local profiles: **`embeddinggemma-2` and `jina-v5` only**. Every other model goes through the OpenAI-compatible profile. |
| P3 | **Default for new folders: `embeddinggemma-2`** (dim 768). Existing folders keep `jina-v5` with no re-embed. Precondition: the Phase 0 GGUF parity passes, because search must not start Python. If it fails, ask the user before shipping a default. |
| P4 | **One active vector space per folder.** A swap is a full re-embed. Old vectors aren't kept. |
| P5 | External services: **one endpoint** (`base_url` + `model`) for both indexing and queries. |
| P6 | API key: **environment variable only**. `config.toml` stores the variable's name and never a secret. |
| P7 | Images with an external service: **provider image extensions** (`jina`, `vllm`, and `llamacpp`, added after Phase 0 found llama-server's multimodal embeddings). If the provider's API accepts images (Jina API style, vLLM multimodal style), image files are embedded in the same space. Otherwise they're keyword-only. Rendered PDF pages for external services are deferred to Plan 4, which picks the tool. |

### 2.1 From grilling (2026-10-07)
| # | Decision |
|---|---|
| S1 | **Shadow build on swap.** The new space's chunks and BM25 are built in side tables. Search keeps using the live index until the rebuild finishes, then the tables are swapped in one transaction and the old ones dropped. Replaces Plan 1's wipe-and-refill `ResetForReembed`. |
| S2 | File changes during a shadow build go **only into the shadow**. The live index is frozen, and `status` shows "N changes pending until the model switch finishes". |
| S3 | Switching again mid-build: **the newest choice wins**, and the partial shadow is discarded. Choosing the live index's own model cancels the build. |
| S4 | Disk check before a shadow build: refuse if free space is under 1.5× the estimate (current index size scaled by the new dim). `--force` overrides. |
| S5 | MCP **cannot** switch models. It reports the model in `status` only. |
| S6 | A ragalay update that bumps a profile's pin moves every folder forward: mismatch banner, then a confirmed shadow re-embed. |
| S7 | Gemma default bar: GGUF↔full parity **mean ≥ 0.999, min ≥ 0.995**. Chunk-eval recall@10 **within 5 points** of Jina, otherwise the numbers go to the user. |
| S8 | Gemma dim **768**. Becomes 256 if Phase 0 shows < 1 point recall loss. |
| S9 | Gemma document title = file name + heading path (`notes.md › Setup › Windows`). Phase 0 checks it against `none`. |
| S10 | Any OpenAI-compatible URL is allowed. A **one-time consent prompt per host per folder** for every non-loopback host (`localhost`, `127.0.0.0/8` and `::1` skip it). `--yes` skips it. |
| S11 | API key UX: the TUI shows per-OS instructions. On Windows it offers to set a user env var (`HKCU\Environment`). |
| S12 | External services get PDF **text** and image **files** only. No rendered PDF pages (Plan 4). |
| S13 | No "same vectors" equivalence between hosts. `embed_id` includes the host. |
| S14 | No nudge for Jina folders to move to Gemma. Release notes only. |
| S15 | `ragalay setup --prune` lists cached models with sizes and removes the chosen ones. Never automatic. |
| S16 | `concurrency`: 1 for remote hosts, 4 for loopback, configurable. |
| S17 | The consent prompt shows chunk, token, and image counts. No price estimates. |
| S18 | Wizard model step: EmbeddingGemma 2 (preselected), Jina v5, "Use another service (advanced)". |

## 3. Background facts (checked 2026-10-07)

| | embeddinggemma-2 | jina-v5 |
|---|---|---|
| Index model | `google/embeddinggemma-2`, 740M (text 270M + vision 170M + audio 300M), encoders can be loaded selectively | `jinaai/jina-embeddings-v5-omni-small-retrieval`, ~1.74B, ~4 GB bf16 |
| Query model | `ggml-org/embeddinggemma-2-GGUF` Q8_0 310 MB / BF16 558 MB, **text-only**, arch `gemma-embedding2`. Parity with the full model's text vectors is **unverified** | text-small GGUF Q8_0 639 MB, same space as omni (parity 0.9997) |
| Dim / Matryoshka | 768 / 128, 256, 512, 768 | 1024 / 32–1024 |
| Context | 8k | 32k |
| Modalities | text, image, video, audio (interleaved) | text, image, PDF page, video, audio |
| Text quality | MMTEB 61.36, MTEB code 78.68 (own card) | MMTEB 67.7 (text-small card) |
| Prompts | `task: search result \| query: {q}` / `title: {t \| none} \| text: {c}` | `Query: ` / `Document: ` |
| Precision | bf16 or fp32, **never fp16** | bf16 on GPU, fp32 on CPU |
| License | Apache 2.0 (card). Phase 0 confirms there are no extra Gemma terms | CC BY-NC 4.0 |

**OpenAI embeddings API.** Request: `{"model", "input": string | string[], "encoding_format": "float" | "base64", "dimensions"?: int}`. Response: `{"data": [{"index", "embedding"}], "model", "usage"}`. The standard is text only. Providers differ on the rest: whether `dimensions` is supported, batch limits (OpenAI: 2048 inputs and 300k tokens per request), max tokens per input, whether vectors are normalized, and whether `base64` is supported.

**Image extensions (Phase 0 verifies the exact shapes):**
- `jina`: `input` is a list of objects, `[{"text": "…"}, {"image": "<url or base64>"}]`, on the same `/v1/embeddings` endpoint
- `vllm`: one request per item with chat-style `messages`, carrying `image_url` content parts (data URIs), for multimodal embedding models served by vLLM

Rendered PDF pages are not sent to external services in Plan 2 (S12).

### 3.1 Phase 0 results (2026-10-07, Windows, RX 9070 XT + 12-thread CPU)

**Runtimes and pins**
- Gemma 2 needs **transformers ≥ 5.19.0** (5.18 doesn't know `embedding_gemma2`). In the setup venv the upgrade replaces only `transformers`, and Jina still loads and embeds on 5.19.
- llama.cpp support landed in **b11454** (`model: support embeddinggemma2`, 2026-10-06). We pin **b11459** for all 5 platforms from ggml-org (ggml-org now ships `ubuntu-arm64`, so the hybridgroup build is no longer needed). yzma moves to **v1.29.0**. Its bindings target llama.cpp v0.6.0, and from there to b11459 `llama.h` only gained one enum value. Plan 1 users must re-run setup once, because v1.29 binds symbols b11146 lacks.
- The Gemma GGUF's `n_embd` is the 512-wide hidden size. The 768-dim output needs `llama_model_n_embd_out`. Pooling is mean, the GGUF's default.
- Jina's parity on b11459 is unchanged: min 0.9996, mean 0.9997, top-1 50/50.
- In one Python process, Jina's remote code breaks Gemma image inputs afterwards ("exactly one of input_ids or inputs_embeds"). This doesn't affect ragalay, which runs one model per sidecar process.

**Parity: Gemma full model vs GGUF, 50 queries, `task: search result | query: ` prompt**
| GGUF | vs CPU fp32 | vs GPU bf16 | ms/query (CPU) |
|---|---|---|---|
| Q8_0 (310 MB) | min 0.99980, mean 0.99989 | min 0.99979, mean 0.99986 | 35 |
| BF16 (558 MB) | 1.00000 | min 0.99995 | 32 |
| Jina text-small Q8_0 (for comparison) | min 0.9996, mean 0.9997 | | 73 |

S7 parity bar met. **Q8_0 is pinned** (half the size).

**Retrieval (`spikes/gemma/eval.py`, 29 queries over the Attention + BERT papers, 256/32-token chunks, page level)**
| variant | hit@1 | hit@5 | recall@10 | MRR |
|---|---|---|---|---|
| Gemma 768, title `none` | 0.83 | 0.93 | 0.97 | 0.870 |
| **Gemma 768, title = file name** | 0.83 | 0.93 | 0.97 | **0.878** |
| Gemma 256 (truncated) | 0.79 | 0.90 | 0.93 | 0.843 |
| Gemma 256, title = file name | 0.83 | 0.90 | 1.00 | 0.866 |
| Gemma page image 1024 px | 0.72 | 0.90 | 1.00 | 0.807 |
| Jina 1024 | 0.69 | 0.93 | 1.00 | 0.807 |
| Jina 256 (truncated) | 0.72 | 0.90 | 0.93 | 0.805 |
| Jina page image 1024 px | 0.69 | 0.97 | 1.00 | 0.807 |

- **S7:** Gemma's recall@10 is 3.4 points under Jina's (one query). That's within 5, so the bar is met. Gemma leads on hit@1 and MRR.
- **S8:** 256 loses 0.027 MRR and 3.4 recall points (more than 1 point), so the **default stays 768**.
- **S9:** a file-name title is slightly better than `none`, so the title is **file name + heading path**. The set is small: one query is 3.4 points.

**Speed (sidecar)**
| | Gemma 2 | Jina v5 omni |
|---|---|---|
| Load (cached) | 5.6 s GPU / 6.7 s CPU | 8.8 s CPU |
| Text chunk, GPU bf16 | 17 ms (batched) | |
| Text chunk, CPU fp32 (PyTorch CPU wheel) | **0.27 s** | 0.63 s |
| Image 1024 px, GPU / CPU | 0.13 s / 4.9 s | 0.10–0.16 s / 4.8 s |
| Peak VRAM (text + vision) | 1.7 GB | ~4 GB |
- The ROCm torch wheel's CPU kernels are 20–30× slower than the CPU wheel (Gemma 8 s and Jina 15 s per chunk, all in `aten::mm`). A machine with a ROCm/CUDA venv should index on the GPU. `--device cpu` there needs the CPU variant.
- Text-only loading (`vision_config: None`) gives identical text vectors (fp32 cos 1.000000).
- `encode_document(image)` applies the document prompt. A caption goes in as `{"text": "title: … | text: <caption> <|image|>", "image": [img]}`.

**OpenAI-compatible matrix (`spikes/openaicompat`)**
| | Ollama 0.20.7 (nomic-embed-text) | llama-server b11459 (Gemma GGUF + mmproj) | vLLM (docs) | Jina API (docs) | OpenAI (docs) |
|---|---|---|---|---|---|
| base64 | ✅ | ✅ | ✅ | ✅ (`embedding_type`) | ✅ |
| `dimensions` | ✅ truncates + renormalizes | ❌ **silently ignored** (returns 768) | ✅ (Matryoshka models) | ✅ | ✅ (v3 models) |
| batch of 64, ordered | ✅ | ✅ | ✅ | ✅ | ✅ (≤ 2048) |
| normalized | ✅ | ✅ | model-dependent | `normalized: true` | ✅ |
| input over context | **silently truncated** to 2048 tokens | HTTP 500 "too large" | 400 | token-batched | 400 |
| unknown model | 404 | accepted (echoes the name) | 404 | 4xx | 404 |
| unknown extra fields | ignored | ignored | — | `task` is meaningful | 400 possible |
| images | ❌ 400 (text model) | ✅ `llamacpp` shape: `{"prompt_string": "<marker>", "multimodal_data": [b64]}`, marker from `GET /props` `media_marker` (random per server). Vectors are cos 0.9665 to the full model's (preprocessing differs) but consistent within the server | `messages` with `image_url` (data URI), `--runner pooling` | `input: [{"image": …}]` | ❌ |

Consequences for the client:
- Always check the returned dimension. `dimensions` may be ignored.
- Keep inputs under `max_input_tokens` (default 2000, under Ollama's silent 2048 cap).
- Normalize every vector.
- Query/document extras must be separate fields (`query_extra` / `document_extra`), because the Jina API needs `task: retrieval.query` vs `retrieval.passage`.
- Add a fourth image mode, `llamacpp`, which gives a fully local multimodal setup without Python.
- Not run: vLLM (no Windows build here), Jina API and OpenAI (no keys), LM Studio (not running). Their rows come from their docs.

**License:** the `google/embeddinggemma-2` card metadata says `apache-2.0`, and the model is not gated.

## 4. Current state in code (what changes)

- `internal/embed` has the `Indexer` and `Querier` interfaces. `embed.ID(model, revision, dim)` names the space. Model choice is hard-wired in several places:
  - `config.supportedDims` holds Jina's list
  - `config.Default()` pins Jina names
  - `setup/pins.go` has a single `queryModel` and `QueryModelID`
  - `sidecar.py` calls Jina's `encode_query`/`encode_document` with `trust_remote_code`
  - `llama.QueryPrefix = "Query: "`
  - `cli/setup.go` asks for the CC BY-NC license
- Chunking counts tokens with the llama query model (`index.Runner.Tokenizer`) and falls back to `chunk.Estimate`.
- `meta.embed_id`, the per-document `embed_id`, the query cache keyed by space plus query model, and `reembed` can be reused as they are.
- Turso doesn't enforce `F32_BLOB(N)`. Writers check the dimension, and that check now comes from the profile.

## 5. Design

### 5.1 Profiles
```go
type Profile struct {
    Name        string            // "embeddinggemma-2", "jina-v5", "openai"
    Modalities  []string          // text, image, pdf_page (+ audio/video in Plan 3)
    Dims        []int             // allowed dims; Matryoshka truncation only within these
    DefaultDim  int
    License     string            // shown once per profile; acceptance asked only for non-commercial licenses
    QueryText   func(q string) string           // applies the profile's query prompt
    DocText     func(title, text string) string // applies the document prompt
    Index       IndexRuntime      // sidecar(model, revision, dtype rules) | http
    Query       QueryRuntime      // llama(GGUF pin) | http
    Tokenizer   TokenizerSource   // llama GGUF | estimate
}
```
- Built-in profiles live in `internal/embed/profiles.go`. Their pins (GGUF URL plus SHA-256, HF revision) live in `setup/pins.go`, keyed by profile.
- `embed_id` = `model@rev7:dim` for local profiles (the Plan 1 format, so Jina's ID is unchanged). For HTTP it's `openai/<model>@<endpoint-host>:<dim>`, plus a short hash of the prefixes, image mode, `dimensions`, and extras when those are set.
  - **Jina keeps today's `embed_id` byte for byte**, so existing folders don't re-embed.
  - Any change goes through the existing mismatch path: exit 2, a banner, then `reembed`.

### 5.2 Config
```toml
[embed]
profile = "embeddinggemma-2"  # embeddinggemma-2 | jina-v5 | openai
dim = 768                     # must be in the profile's Dims (validated)
image_max_side = 1024

[embed.openai]                # read only when profile = "openai"
base_url = "http://localhost:11434/v1"
model = "nomic-embed-text"
api_key_env = "OPENAI_API_KEY"  # env var NAME; empty = no Authorization header (local servers)
dimensions = 0                # 0 = don't send; >0 = send `dimensions`
matryoshka = false            # true lets ragalay truncate to `dim` itself
image_input = "none"          # none | jina | vllm | llamacpp
query_prefix = ""             # e.g. "search_query: " for nomic
document_prefix = ""
batch_size = 64
max_input_tokens = 8000
timeout = "30s"               # indexing requests
query_timeout = "5s"          # search falls back to keyword-only after this
query_extra = {}              # merged into query requests (e.g. {"task": "retrieval.query"})
document_extra = {}           # merged into document requests (e.g. {"task": "retrieval.passage"})
concurrency = 0               # 0 = 4 on this computer, 1 elsewhere (S16)
```
- **Migrating existing folders:** if a config has the old keys (`index_model` / `index_revision` / `query_model`), **or** the folder's `meta.embed_id` is a Jina space, `profile = "jina-v5"` is written explicitly on load. An old folder never silently switches to the Gemma default.
- The key is read from the env var at call time. Nothing secret is written to the folder, the logs, or `status` output.

### 5.3 OpenAI-compatible client (`internal/embed/openai`)
- It implements `Indexer` and `Querier` with plain `net/http` (no CGO).
- It batches by count and by estimated tokens. It retries 429 and 5xx with backoff, honoring `Retry-After`. The job queue makes progress resumable per file.
- It asks for `base64` and falls back to `float`. Every vector goes through `embed.Fit`, which normalizes it and truncates it only when `matryoshka = true`. A wrong dimension fails loudly.
- **`ragalay model test`** sends one text, plus one image if `image_input` is set. It reports latency, dimension, whether the vector is normalized, and whether images worked, then saves the detected dimension.
- **Privacy and safety:**
  - The first bulk send to a non-loopback host asks once per host per folder: "N chunks (~T tokens) and M images will be sent to <host>". The answer is recorded in `meta`, and `--yes` covers agents and scripts (S10, S17).
  - `http://` to a non-loopback host gets a warning.
  - Request bodies and keys are never logged.
- **Search:** Python is still never started. If the endpoint fails or exceeds `query_timeout`, search falls back to keyword-only with a notice, as it does today without llama.cpp. The query cache cuts repeat calls.

### 5.4 Images and PDF pages per profile
| Profile | image / pdf_page |
|---|---|
| embeddinggemma-2, jina-v5 | Python sidecar, as today |
| openai + `image_input = jina \| vllm` | Go reads the image file, resizes it to `image_max_side`, and sends it base64 over HTTP. PDF pages are text only (S12) |
| openai + `image_input = none` | Not vector-embedded. Still keyword-searchable through captions, alt text, linked notes, and file names. `status` and the TUI say "images: keyword-only (model is text-only)" |

### 5.5 Sidecar generalization
- `sidecar.py` gets one adapter per local profile, covering loading, dtype rules, encode, and prompts.
  - Gemma: sentence-transformers with its prompts. Only the text and vision encoders are loaded (audio comes in Plan 3). It never uses fp16, so CUDA/ROCm run bf16 and CPU runs fp32.
  - Jina: unchanged behavior.
- `ragalay setup` installs only the active profile's weights and GGUF. A profile that is only HTTP needs no Python, no llama.cpp, and no downloads. Switching to a local profile that isn't installed yet runs the download prompt.
- llama.cpp / yzma pin: bump it if `gemma-embedding2` needs a build newer than `b11146`, then re-run the Jina parity check on the new build.

### 5.6 Shadow build (S1–S4)
- `ragalay model use` / `reembed` creates `chunks_next`, `postings_next`, and `terms_next`, and records the target space in `meta.next_embed_id`.
- The indexer embeds into the shadow while it exists. Scan changes during the build go to the shadow only (S2).
- When nothing is left to embed, one transaction renames the shadow tables over the live ones, sets `meta.embed_id`, clears `query_cache`, and drops the old tables.
- A new switch while building (S3) drops the shadow and starts again. Choosing the live model just drops the shadow.
- A fresh or empty index needs no shadow: the build goes straight to live.
- Search reads only the live tables, with the live profile's querier: the settings recorded in `meta.embed_config`, or inferred from a local profile's `embed_id` for Plan 1 indexes. Long-lived searchers (TUI, MCP) reload the query model when the live space changes.

### 5.7 Tokenizer
Local profiles count tokens with their GGUF tokenizer. The HTTP profile uses `chunk.Estimate` with a margin under `max_input_tokens`.

### 5.8 UX
```
ragalay model list [--json]                     # profiles, active, license, modalities, installed?
ragalay model show [--json]                     # active profile, embed_id, endpoint host, remote?, image mode
ragalay model use <profile> [--dim N] [--yes]   # writes config, installs if needed, queues reembed
ragalay model test [--json]                     # probe the active runtime / endpoint
```
- TUI: a **Model** view to pick a profile, enter `base_url`, `model`, the key's env-var name, and the image mode, run the probe, and confirm the re-embed with its ETA. It shows key help per OS, and on Windows it offers to set the user env var (S11).
- The setup wizard gets a model step (S18).
- `ragalay setup --prune` (S15).
- `status --json` and MCP `status` add `embed: {profile, model, dim, endpoint_host, remote, modalities, image_mode}`.

### 5.9 Conventions (CLAUDE.md and README)
- Network rule becomes: "the only network calls are setup downloads, the opt-out update check, **and the embedding endpoint the user configures**."
- The CLAUDE.md project summary is updated: Gemma 2 is the default and Jina is optional.
- README: a non-technical section, "Use a different model". It covers the built-ins first, then Ollama and LM Studio, then online services with a privacy note. The license wording becomes per profile.

## 6. Risks
- **Gemma GGUF parity** might fail. That blocks P3 (see the precondition). Fallback: keep Jina as the default and ship Gemma as an option only if its GGUF and full model agree.
- Gemma's text retrieval scores lower than Jina's on published benchmarks. Phase 0 measures both on our chunk-eval set, and the result is shown to the user before Phase 1.
- OpenAI-compatible servers vary: no `dimensions`, no `base64`, silent truncation, unnormalized vectors, image formats that don't match the docs. The probe and conservative defaults have to cover these.
- Cost and privacy with online providers. Mitigated by the consent prompt and token estimates.
- A llama.cpp pin bump may shift Jina's parity. It gets re-checked in Phase 0.

## 7. Phases

### Phase 0: Spikes ✅ Completed (2026-10-07)
- [x] The Gemma 2 GGUF loads in the pinned llama.cpp/yzma. It doesn't on b11146: the minimum is b11454, so b11459 + yzma v1.29.0 are pinned, and Jina parity was re-checked there (0.9997)
- [x] Parity: Q8_0 min 0.99980 / mean 0.99989, BF16 1.0000. **Q8_0** pinned
- [x] Gemma sidecar: CPU fp32 0.27 s/chunk, 4.9 s/image (CPU wheel). GPU 17 ms/chunk, 0.13 s/page image. Text-only loading gives identical vectors. Prompts confirmed
- [x] Retrieval on the chunk-eval set: Gemma vs Jina (§3.1). The OpenAI-compatible model was tested for API behavior, not retrieval quality, since quality depends on whichever model the user runs
- [x] OpenAI-compatible matrix: Ollama and llama-server run, vLLM / OpenAI / Jina API from docs, LM Studio not available (§3.1)
- [x] Image extensions: Jina API and vLLM shapes from docs, llama-server shape tested (new `llamacpp` mode). go-pdfium page rendering is moot (S12, Plan 4)
- [x] Gemma 2 license: Apache 2.0 on the card, not gated
- Exit: numbers recorded in §3.1. S7 met (parity and retrieval within 5 points), so P3 stands and Gemma is the default

### Phase 1: Profiles and config ✅ Completed (2026-10-07)
- [x] `embed.Profile` plus the registry (`embeddinggemma-2`, `jina-v5`, `openai`). Per-profile pins (`setup.queryModels`, `indexModelSizes`), llama.cpp b11459, yzma v1.29.0, transformers 5.19.0
- [x] Config `[embed] profile` and `[embed.openai]`, per-profile validation, migration of old keys (§5.2). The meta-based fallback was dropped: every Plan 1 `config.toml` has `index_model`, so the key migration covers every Plan 1 folder
- [x] `embed_id` from the profile (Jina byte-identical, tested). Dimension check from the profile. Removed the Jina-only constants (`supportedDims`, `llama.QueryPrefix`, `setup.QueryModelID`)
- [x] License prompt per profile (only Jina needs acceptance; Gemma shows an Apache 2.0 note). `setup.State` keeps installs per profile and migrates Plan 1's `state.json`. `Ready` requires the pinned llama.cpp, so Plan 1 machines re-run setup once. Library-only pin changes upgrade the venv in place instead of re-downloading PyTorch
- Exit: an existing Jina folder loads unchanged, with the same `embed_id` and no re-embed. A new folder gets Gemma 2 at 768. `go test ./...` passes

### Phase 2: Gemma 2 local profile ✅ Completed (2026-10-07)
- [x] Sidecar adapters (Gemma, Jina), dtype rules (Gemma: bf16 on GPUs that support it, else fp32; never fp16), prompts on both sides, title = file name + heading path, images as interleaved `<|image|>` input
- [x] Setup installs the active profile only. llama.cpp pin bumped to b11459 (Phase 1). Real run on Windows/ROCm: library upgrade in place, self-test parity **0.9999**
- [x] `TestBothRuntimesOnThisMachine` runs per installed profile (skipped without setup). Both pass on this machine: Gemma query vs attention text 0.868 / bread 0.559, Jina 0.853 / 0.045. `TestDoubleClickToSearch` passes on the Gemma default
- Exit check: a Gemma folder indexed MD + PDF + linked image (71 chunks, 26 s), and searches found the right page, note, and image with llama.cpp only
- Exit: a Gemma folder indexes MD, PDF, and images. Search parity is at or above the Phase 0 threshold, and search uses no Python

### Phase 3: Shadow build ✅ Completed (2026-10-07)
- [x] Shadow tables (`chunks_next`, `terms_next`, `postings_next`) plus `meta.next_embed_id` / `next_embed_dim` / `next_embed_config`. `store.WriteTarget` sends every chunk write (including linked-image cleanup) to the shadow while a switch runs, and deletes leave both indexes. Schema v3 adds `shadow_dirty`
- [x] Atomic swap at the end of an indexing run (`FinishShadow`: drop live, rename shadow, recreate indexes, clear the query cache). A new choice restarts the switch, and choosing the live model cancels it, requeueing only documents that changed during the switch (S3). An index with no chunks rebuilds in place
- [x] Disk check (S4): index size scaled by the new dim, refuse under 1.5× free, `--force` overrides. `status` (text and `--json` `model_switch`) and the TUI show progress and pending changes (S2)
- [x] Tests: store lifecycle (start/write/delete/finish/cancel/restart), runner switch with an interruption (live chunks untouched at the old dim), resume and swap, re-switch and cancel, low-disk refusal, in-place rebuild of an empty index
- Exit check (real models, Windows): Gemma → Jina → Gemma on a folder with MD + PDF + image. Search answered throughout both switches, with vector search on the previous model during the second, and the swap happened when indexing finished
- Also found and fixed:
  - Schema migrations only ran at `init`. They now run when any command opens a folder (`cli.upgradeIndex`), so Plan 1 folders get v3.
  - Plan 1 indexes don't record their settings. `index.LiveEmbed` infers a local profile from its `embed_id`, so search keeps its vectors during a switch, and setup now records `embed_config`.
- **Behavior change (plan1 §4.7):** `search` no longer exits 2 on a model mismatch. It keeps using the live index's model and adds a notice (and `--mode vector` with an unknown live model fails with `ErrOtherSpace`). `scan` and `reembed` still exit 2 on a mismatch.

### Phase 4: OpenAI-compatible client ✅ Completed (2026-10-07)
- [x] `internal/embed/openai`, plain `net/http`:
  - batching and `concurrency`, results reordered by index
  - retries for 429/5xx/timeouts with `Retry-After`; refused connections and unknown hosts fail at once with "is the embedding server running?" (Windows reports refusals differently, so any failed dial counts)
  - base64 with a float fallback
  - `dimensions`; without `matryoshka`, a wrong vector size is an error that names the right `dim`
  - `query_extra` / `document_extra`, input clipping under `max_input_tokens`, per-request and query timeouts
  - the key only in the `Authorization` header and redacted from errors
- [x] Image extensions `jina`, `vllm`, `llamacpp` (media marker from `/props`). Go-side image loading with `golang.org/x/image` (PNG, JPEG, GIF, WebP, BMP, TIFF; CatmullRom scaling to `image_max_side`). PDF page images are dropped for services (S12). With `none`, images stay keyword-only: no vector, file name as text when there is no caption
- [x] Search through the HTTP querier. A failure or `query_timeout` falls back to keywords (existing search fallback)
- [x] Consent recorded per host per folder (meta `consent_upload:<host>`), asked once with document, token, and image counts (S17). `--yes` on `scan` and `reembed`; without a terminal the command explains `--yes`. Warning for `http://` to another computer
- [x] Tests: `httptest` server for batching and order, base64 rejection → floats, 429 retries, wrong count, refused connection, concurrency cap, query timeout, clipping, all three image shapes, image scaling. CLI tests for consent and a full local-service run with an empty model cache
- Exit check (real servers, Windows): with an empty model cache, a folder indexed and searched through Ollama 0.20.7 (`nomic-embed-text`, text, image keyword-only by file name), and another through llama-server b11459 with Gemma + mmproj (`llamacpp` image mode: vector search found `IMG_0042.jpg` for "a red sports car"). No Python, nothing downloaded
- Also fixed: changing the model between `init` and the first scan no longer reports a mismatch. An index with no chunks adopts the new settings

### Phase 5: CLI, TUI, MCP
- [ ] `ragalay model list|show|use|test` with `--json`. `ragalay setup --prune`
- [ ] TUI Model view (key help, Windows env-var offer) and the wizard model step. Re-embed banner and flow reused
- [ ] `status --json` and MCP `status` report the embed profile, whether it's remote, and the image mode
- Exit: switching Gemma → Ollama → Jina → Gemma works from both the CLI and the TUI, each switch re-embedding with a correct ETA while search stays complete

### Phase 6: Docs and release
- [ ] README: "Use a different model" (non-technical first), per-profile licenses, privacy note
- [ ] CLAUDE.md: project summary, models layout, network-call rule
- [ ] Release notes, including "existing folders keep Jina"
- Exit: README reviewed. A tagged release builds on all 5 platforms
