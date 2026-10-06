# ragalay

**Search your own documents by asking questions.** Put ragalay in a folder with your notes, PDFs and pictures, double-click it, and type what you are looking for: "what did the contract say about notice periods?", "the photo of the red car", "how many layers does the encoder have?". ragalay finds the right passage, page or image.

Everything runs on your computer. Your documents never leave it.

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
2. **The AI models' license**: ragalay uses two free models from Jina AI. They are free for **personal and research use**, but **not for commercial use**. Press `y` to accept.
3. **The one-time download**: about 4 to 8 GB, depending on your computer. It goes into one shared place, so other folders you use ragalay in later do not download it again. Leave the window open; if the internet drops, start ragalay again and it carries on where it stopped.

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
| Tab | switch between **Search**, **Status** and **Folders** |
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

## What computer do I need?

| Computer | Reading documents | Searching |
|---|---|---|
| NVIDIA graphics card | fast | instant |
| AMD Radeon RX 9070 / 9060 (Windows) | fast (about 0.1 s per page) | instant |
| Mac with Apple chip | uses the Mac's graphics chip | instant |
| Anything else | works, but slowly: about 1.5 s per text passage and 4 to 7 s per PDF page | instant |

Searching always runs on the processor and takes about a second. Free disk space: about 10 GB for the one-time download, plus a little for each folder's index.

On a computer without a supported graphics card, you can make ragalay skip the slow "page pictures" of PDFs: set `pdf_page_images = false` under `[index]` in `.ragalay/config.toml`. Text in PDFs is still searchable; scanned pages are not.

---

## Privacy

- Your documents and searches stay on your computer.
- **No telemetry**: ragalay sends nothing about you or your use.
- ragalay only goes online to (a) download the models during setup and (b) check once a day whether a newer ragalay exists. Turn (b) off with `check = false` under `[update]` in `.ragalay/config.toml`.

---

## Licenses

- **ragalay** is open source under the [Apache License 2.0](LICENSE).
- **The AI models** (`jina-embeddings-v5-omni-small-retrieval` and `jina-embeddings-v5-text-small-retrieval` by Jina AI) are licensed [CC BY-NC 4.0](https://creativecommons.org/licenses/by-nc/4.0/): **personal and research use only, not commercial use.** ragalay downloads them for you after you accept this.

---

## Something not working?

| Problem | What to do |
|---|---|
| A document is missing from results | Open the **Status** view: it lists files that could not be read and why. |
| "Another ragalay is indexing" | Only one ragalay at a time reads a folder. Close the other window (or wait for it). |
| "The model settings changed" | Press `R` to rebuild the index (needed after changing `dim` in the settings). |
| Setup stopped halfway | Start ragalay again; it continues where it stopped. |
| Anything else | Look in `.ragalay/logs/` inside your folder (`setup.log`, `index.log`, `sidecar.log`). |

**Updating**: run `ragalay update` (or download the new version and replace the old file). Your index and models are kept.

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
ragalay folders add|remove [--keep]|list [--json]
ragalay scan [--watch] [--no-index] [--json]   find changes and index them
ragalay search "query" [-k 10] [--mode hybrid|vector|keyword] [--modality text,image,pdf_page]
               [--path-glob "papers/*"] [--group-by chunk|doc] [--max-chars 2000] [--json]
ragalay docs [--status failed] [--kind pdf] [--json]
ragalay status [--json]
ragalay reembed [--force]             rebuild after changing the model settings
ragalay mcp                           MCP server on stdio
ragalay update [--check] | version
```

`--root <folder>` (or `RAGALAY_ROOT`) chooses the folder; otherwise ragalay looks upward from the current folder, then from where the program is.

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
| 2 | the model settings changed: run `ragalay reembed` |
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

The search model stays loaded for the session, so searches after the first take well under a second. Search never starts Python; only indexing (`scan`) does.

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
dim = 1024                   # 1024, 768, 512, 256, 128, 64 or 32; changing it needs `ragalay reembed`

[cache]
query_max = 10000            # remembered search embeddings
query_ttl = "720h"

[update]
check = true                 # daily check for a newer ragalay
```

### How it works

- Indexing: Markdown is split by headings into ~256-token passages; PDFs give their text per page plus an image of every page; pictures are embedded as images. Everything is embedded with **jina-embeddings-v5-omni-small** in a Python helper that ragalay installs and manages (PyTorch on CUDA, ROCm, Apple MPS or CPU).
- Search: the question is embedded with **jina-embeddings-v5-text-small** through llama.cpp inside ragalay; it shares the omni model's vector space (cosine 0.9997 in our checks), so no Python is needed. Vector search and BM25 keyword search run in the embedded [Turso](https://turso.tech) database and are merged with reciprocal rank fusion.
- Design notes and measurements: [`plans/`](plans/).

### Building from source

Go 1.26 or newer; no C compiler needed.

```sh
go build ./cmd/ragalay
go test -short ./...   # without -short, also runs tests that use the real models if `ragalay setup` has run
```
