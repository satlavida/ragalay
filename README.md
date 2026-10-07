# ragalay

**Search your own documents by asking questions.** Put ragalay in a folder with your notes, PDFs and pictures, double-click it, and type what you are looking for: "what did the contract say about notice periods?", "the photo of the red car", "how many layers does the encoder have?". ragalay finds the right passage, page or image.

Everything runs on your computer. Your documents never leave it (unless you choose to connect ragalay to an online AI service, see [Use a different model](#use-a-different-model)).

---

## Get started (5 minutes, plus a one-time download)

### 1. Download ragalay

Go to the [Releases page](https://github.com/satlavida/ragalay/releases/latest) and download the file for your computer:

| Your computer | Download |
|---|---|
| Windows | `ragalay_windows_amd64.zip` |
| Mac with Apple chip (M1, M2, M3, M4) | `ragalay_darwin_arm64.tar.gz` |
| Mac with Intel chip | `ragalay_darwin_amd64.tar.gz` |
| Linux | `ragalay_linux_amd64.tar.gz` (or `_arm64`) |

Not sure which Mac you have? Apple menu → **About This Mac**. "Chip: Apple M…" means Apple chip.

Open the downloaded file to unpack it. Inside is the program, `ragalay` (`ragalay.exe` on Windows).

### 2. Put it in your documents folder

Move `ragalay` into the folder you want to search, for example `Documents`. ragalay searches that folder and everything inside it (you can narrow this down later).

### 3. Double-click it

The first time, your computer may warn you, because ragalay is not (yet) signed by Apple or Microsoft:

- **Windows** shows "Windows protected your PC". Click **More info**, then **Run anyway**.
- **Mac** says it "cannot be opened because the developer cannot be verified". Click **Done**, then open **System Settings → Privacy & Security**, scroll down to "ragalay was blocked", and click **Open Anyway**. (Or: right-click ragalay, choose **Open**, then **Open**.)

You only need to do this once.

### 4. Follow the setup screens

A window opens and walks you through:

1. **Which folders to search**: everything, or just some folders. Use the arrow keys and the space bar, then Enter.
2. **Which AI model to use**: just press Enter for **EmbeddingGemma 2**, a free model from Google that you may use for anything, including work. (The other choices are explained in [Use a different model](#use-a-different-model).)
3. **The model's license**: a short note about the model's license. Press `y` to continue.
4. **The one-time download**: about 2.5 to 6 GB, depending on your computer. It goes into one shared place, so other folders you use ragalay in later do not download it again. Leave the window open; if the internet drops, start ragalay again and it carries on where it stopped.

Then ragalay reads your documents. You can search straight away; results get better as it works through the rest.

### 5. Search

Type a question or a few words and press **Enter**.

```
ragalay  [Search]  Status   Folders    42 documents indexed

🔎 how many layers does the encoder have

>  1. papers/attention.pdf · p.3
     The encoder is composed of a stack of N = 6 identical layers…
   2. notes/transformer.md
     The encoder is a stack of six identical layers, each with multi-head self-attention.
╭──────────────────────────────────────────────────────────────────────────╮
│ papers/attention.pdf — Page 3                                            │
│ The encoder is composed of a stack of N = 6 identical layers. Each layer │
│ has two sub-layers…                                                      │
╰──────────────────────────────────────────────────────────────────────────╯
↑/↓: choose   Enter: open file   o: open image   /: new search   Tab: next view   q: quit
```

| Key | What it does |
|---|---|
| Enter (in the search box) | search |
| ↓ or Esc | move to the results |
| ↑ / ↓ | choose a result; the box below shows it in full |
| Enter (on a result) | open the file |
| `o` | open the picture itself (for pictures inside a Markdown file) |
| `/` | new search |
| Tab | switch between **Search**, **Status**, **Folders** and **Model** |
| `r` | look for new or changed files now |
| `q` or Ctrl+C | quit |

---

## Everyday use

**Adding documents**: just put files in the folder. While ragalay is open it notices new, changed, moved and deleted files within seconds; otherwise it catches up the next time you open it. Moving or renaming a file does not make it read the file again.

**What ragalay reads**: Markdown (`.md`), PDF (including scanned PDFs, which it reads as pictures of the pages), and pictures (`.png`, `.jpg`, `.webp`, `.gif`, `.bmp`).

**Pictures in Markdown files**: keep the pictures a Markdown file shows **in the same folder as the Markdown file** (or a subfolder such as `images/` or `assets/`). ragalay then indexes each picture together with its caption and the heading it sits under, and search results say which file it appears in.

**Typed-up copies of PDFs**: if you keep a Markdown transcription of a PDF, ragalay links the two and shows one result that points to the PDF page and shows the transcription's text. It pairs them when:
1. the Markdown file starts with `source: path/to/file.pdf` in its front matter, or
2. you list matching folders in `.ragalay/config.toml`:
   ```toml
   [[pairs]]
   md = "transcripts"
   pdf = "scans"
   ```
3. or they simply have the same name (`report.md` and `report.pdf`).

**Choosing folders**: in the **Folders** view, `a` adds a folder, `d` removes it (its documents leave the search), `x` removes it but keeps what is already indexed searchable.

**Moving the whole folder**: fine. ragalay keeps its index in a hidden `.ragalay` folder inside, with relative paths, so copy or move the folder anywhere and it still works.

---

## Use a different model

The **AI model** is what lets ragalay understand meaning, so that "notice period" finds "termination with 30 days' warning". Each folder uses one model. To change it, open the **Model** view (press Tab until it is highlighted), choose a model and press Enter.

| Model | Good for | License |
|---|---|---|
| **EmbeddingGemma 2** (default) | everyday use; fast, about 1.8 GB | Apache 2.0: free for any use, including work |
| **Jina v5** | scores a little higher on published search benchmarks; larger and slower, about 4.9 GB | CC BY-NC 4.0: **personal and research use only** |
| **Another service** | a model you already run, or an online AI service | depends on the service |

**Changing the model re-reads all your documents.** You can keep searching while it does: results come from the old model until the new one is ready, then ragalay switches over by itself. A big folder can take a while (see [What computer do I need?](#what-computer-do-i-need)).

### Another service

Choose **Another service** to use any program or website that offers "OpenAI-compatible embeddings". Fill in:

- **Address**: for example `http://localhost:11434/v1` for [Ollama](https://ollama.com), `http://localhost:1234/v1` for [LM Studio](https://lmstudio.ai), `https://api.openai.com/v1` for OpenAI.
- **Model**: the model's name in that program, for example `nomic-embed-text` in Ollama (install it first with `ollama pull nomic-embed-text`).
- **Key name**: online services need an API key. ragalay never saves the key itself. You give the *name* of a setting on your computer that holds it, for example `OPENAI_API_KEY`. On Windows, press **Ctrl+K** in the form and paste the key: ragalay stores it for your user account. On Mac and Linux, add `export OPENAI_API_KEY="your key"` to `~/.zshrc` (or `~/.bashrc`) and open a new terminal.
- **Images**: leave it on `none` unless the service can read pictures. With `none`, pictures are still found by their file name and caption.

ragalay checks that the service answers before it switches.

**Privacy**: a service on your own computer (an address starting with `http://localhost` or `http://127.0.0.1`) keeps everything local. **Any other address sends your documents and searches to that computer or company.** ragalay asks you once per folder before it sends anything, and tells you how much will be sent. Online services may charge for this.

---

## What computer do I need?

| Computer | Reading documents | Searching |
|---|---|---|
| NVIDIA graphics card | fast | instant |
| AMD Radeon RX 9070 / 9060 (Windows) | fast (about 0.1 s per page) | instant |
| Mac with Apple chip | uses the Mac's graphics chip | instant |
| Anything else | works, more slowly: about 0.3 s per text passage and 5 s per PDF page (Jina v5: 0.6 s per passage) | instant |

Searching always runs on the processor and takes well under a second. Free disk space: about 6 GB for the one-time download with the default model (about 10 GB with Jina v5), plus a little for each folder's index. While a folder switches models it needs about as much space again as its index.

With **another service**, your computer does not run a model at all: nothing is downloaded, and the speed depends on the service.

On a computer without a supported graphics card, you can make ragalay skip the slow "page pictures" of PDFs: set `pdf_page_images = false` under `[index]` in `.ragalay/config.toml`. Text in PDFs is still searchable; scanned pages are not.

---

## Privacy

- Your documents and searches stay on your computer, unless you connect a folder to a service on another computer (see [Another service](#another-service)); ragalay asks before it sends anything there.
- **No telemetry**: ragalay sends nothing about you or your use.
- ragalay only goes online to (a) download the models during setup, (b) check once a day whether a newer ragalay exists, and (c) reach the embedding service you chose, if any. Turn (b) off with `check = false` under `[update]` in `.ragalay/config.toml`.

---

## Licenses

- **ragalay** is open source under the [Apache License 2.0](LICENSE).
- **EmbeddingGemma 2** (`google/embeddinggemma-2` by Google, the default) is licensed [Apache 2.0](https://www.apache.org/licenses/LICENSE-2.0): free for any use, including commercial.
- **Jina v5** (`jina-embeddings-v5-omni-small-retrieval` and `jina-embeddings-v5-text-small-retrieval` by Jina AI) is licensed [CC BY-NC 4.0](https://creativecommons.org/licenses/by-nc/4.0/): **personal and research use only, not commercial use.** ragalay only downloads it if you choose it and accept this.
- **Another service**: its own terms apply.

---

## Something not working?

| Problem | What to do |
|---|---|
| A document is missing from results | Open the **Status** view: it lists files that could not be read and why. |
| "Another ragalay is indexing" | Only one ragalay at a time reads a folder. Close the other window (or wait for it). |
| "The model settings changed" | Press `R` to switch the index to the new settings (needed after editing the model in the settings file). Search keeps working meanwhile. |
| "The service did not answer" | Check that the program (Ollama, LM Studio, …) is running and the address and model name are right. `ragalay model test` shows what it answers. |
| "The API key environment variable … is not set" | Set the key as described in [Another service](#another-service), then open a new window. |
| Setup stopped halfway | Start ragalay again; it continues where it stopped. |
| Anything else | Look in `.ragalay/logs/` inside your folder (`setup.log`, `index.log`, `sidecar.log`). |

**Updating**: run `ragalay update` (or download the new version and replace the old file). Your index and models are kept. After some updates ragalay asks for a short setup again (for example, a newer search engine).

**Freeing disk space**: `ragalay setup --prune` lists the models on this computer with their sizes and lets you remove the ones you no longer use.

**Removing ragalay**: delete the program, the hidden `.ragalay` folder in each folder you used it in, and the shared download folder:
- Windows: `%LOCALAPPDATA%\ragalay`
- Mac: `~/Library/Caches/ragalay`
- Linux: `~/.cache/ragalay`

The indexing model also sits in the Hugging Face cache (`~/.cache/huggingface`), shared with other AI tools you may use.

---

## For AI agents and power users

Everything the window does is also a command. Run `ragalay <command> --help` for details.

```text
ragalay init [folder]                 make a folder searchable (creates .ragalay/)
ragalay setup [--device auto|cpu|cuda|mps|rocm-gfx1201] [--accept-license --yes]
ragalay setup --prune | --remove <model> [--yes]   list / delete downloaded models
ragalay model list|show|test [--json]
ragalay model use <embeddinggemma-2|jina-v5|openai> [--dim N] [--yes] [--no-index]
               [--base-url URL --model NAME --api-key-env VAR --image-input none|jina|vllm|llamacpp
                --query-prefix TEXT --document-prefix TEXT --dimensions N --matryoshka]
ragalay folders add|remove [--keep]|list [--json]
ragalay scan [--watch] [--no-index] [--yes] [--json]   find changes and index them
ragalay search "query" [-k 10] [--mode hybrid|vector|keyword] [--modality text,image,pdf_page]
               [--path-glob "papers/*"] [--group-by chunk|doc] [--max-chars 2000] [--json]
ragalay docs [--status failed] [--kind pdf] [--json]
ragalay status [--json]
ragalay reembed [--force] [--yes]     switch the index to the current model settings
ragalay mcp                           MCP server on stdio
ragalay update [--check] | version
```

`--root <folder>` (or `RAGALAY_ROOT`) chooses the folder; otherwise ragalay looks upward from the current folder, then from where the program is.

`--yes` on `scan`, `reembed` and `model use` allows sending documents to a service on another computer (asked once per folder otherwise; without a terminal the command stops and says so).

### Search output (`search --json`)

A JSON array, most relevant first:

```json
[
  {
    "score": 0.0328,
    "path": "papers/attention.pdf",
    "kind": "pdf",
    "modality": "text",
    "page": 3,
    "heading_path": "",
    "text": "The encoder is composed of a stack of N = 6 identical layers…",
    "paired_path": "transcripts/attention.md",
    "parent_path": ""
  }
]
```

- `modality` is `text`, `image` or `pdf_page` (a page matched as a picture; `text` is empty, cite the page).
- `parent_path` is set for pictures inside a Markdown file (`path` is the picture).
- `paired_path` is the Markdown transcription of a PDF (or the PDF of a transcription).
- Scores are for ordering only (reciprocal rank fusion in hybrid mode).

### Exit codes

| Code | Meaning |
|---|---|
| 0 | OK |
| 1 | error |
| 2 | `scan`/`reembed`: the model settings name a model the index was not built with; run `ragalay reembed` (or `ragalay model use …`). `search` does not use this code: it keeps answering with the index's model and adds a `notice` |
| 3 | not a ragalay folder: run `ragalay init` |
| 4 | setup incomplete (or license not accepted) |
| 5 | another ragalay process is indexing |

### MCP (Claude Code, Claude Desktop, other agents)

ragalay speaks the [Model Context Protocol](https://modelcontextprotocol.io) on stdio with the tools `search`, `status`, `list_documents`, `list_folders` and `scan`.

Claude Code:

```sh
claude mcp add ragalay -- /path/to/ragalay mcp --root /path/to/your/documents
```

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "ragalay": {
      "command": "/path/to/ragalay",
      "args": ["mcp", "--root", "/path/to/your/documents"]
    }
  }
}
```

The search model stays loaded for the session, so searches after the first take well under a second; after a model switch finishes, the next search loads the new one. Search never starts Python; only indexing (`scan`) with a local model does. `status` includes the folder's model (`embed`: profile, model, dim, endpoint host, whether data leaves the computer, image mode) and any switch in progress (`model_switch`). Agents cannot change the model over MCP.

### Settings (`.ragalay/config.toml`)

```toml
[scan]
folders = []                 # relative folders to index; empty = everything
keep = []                    # folders no longer scanned whose documents stay searchable
ignore = ["**/node_modules/**", "**/.git/**"]
kinds = ["md", "pdf", "image"]

[[pairs]]                    # optional: Markdown transcriptions ↔ PDFs
md = "transcripts"
pdf = "scans"

[index]
pdf_page_images = true       # also embed PDF pages as pictures (finds scans and figures)

[chunk]
tokens = 256                 # passage size
overlap = 32

[embed]
profile = "embeddinggemma-2" # embeddinggemma-2 | jina-v5 | openai  (use `ragalay model use`)
dim = 768                    # gemma: 768/512/256/128; jina: 1024…32; a service: its vector size
image_max_side = 1024

[embed.openai]               # only for profile = "openai"
base_url = "http://localhost:11434/v1"
model = "nomic-embed-text"
api_key_env = ""             # NAME of the environment variable with the key; empty = no key
image_input = "none"         # none | jina | vllm | llamacpp
query_prefix = ""            # e.g. "search_query: " for nomic models
document_prefix = ""
dimensions = 0               # >0: send "dimensions" (services that support it)
matryoshka = false           # true: ragalay may cut vectors to `dim`
batch_size = 64
concurrency = 0              # requests at once; 0 = 4 on this computer, 1 elsewhere
max_input_tokens = 2000
timeout = "1m0s"             # per indexing request
query_timeout = "5s"         # search falls back to keywords after this
# query_extra / document_extra = { task = "..." }   # extra request fields per side (e.g. Jina's API)

[cache]
query_max = 10000            # remembered search embeddings
query_ttl = "720h"

[update]
check = true                 # daily check for a newer ragalay
```

### How it works

- Indexing: Markdown is split by headings into ~256-token passages; PDFs give their text per page plus an image of every page; pictures are embedded as images. With a local model, everything is embedded by **EmbeddingGemma 2** or **jina-embeddings-v5-omni-small** in a Python helper that ragalay installs and manages (PyTorch on CUDA, ROCm, Apple MPS or CPU). With a service, ragalay sends text (and pictures, if the service takes them) over its OpenAI-compatible API; PDF page pictures are not sent.
- Search: the question is embedded through llama.cpp inside ragalay with the model's GGUF (EmbeddingGemma 2 Q8_0, cosine 0.9999 to the full model; jina-embeddings-v5-text-small, 0.9997), or by the service. No Python is involved. Vector search and BM25 keyword search run in the embedded [Turso](https://turso.tech) database and are merged with reciprocal rank fusion.
- Model switches build the new index next to the old one and swap them in one step when it is complete, so search always sees a complete index.
- Design notes and measurements: [`plans/`](plans/).

### Building from source

Go 1.26 or newer; no C compiler needed.

```sh
go build ./cmd/ragalay
go test -short ./...   # without -short, also runs tests that use the real models if `ragalay setup` has run
```
