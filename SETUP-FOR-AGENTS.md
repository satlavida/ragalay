# Setting up ragalay for someone (guide for AI agents)

You are an AI agent (Claude Code or similar) setting ragalay up on the user's computer: download, install, first index, connect it to yourself, and show the user how to use it. Work through the steps in order. Each step ends on a **Done when** line. Reach it before you move on.

ragalay is one program that sits in a folder of documents (Markdown, PDF, images) and makes them searchable by meaning. People use its window. You use its CLI (`--json`) or its MCP server. For what it is and how people use it, see [README.md](README.md). Every command has `--help`. Treat that as the authority for flags.

## Rules for the whole setup

- **The user decides** which folder, which model, the download, and whether documents may go to another computer. Ask, then act. Everything else has a sensible default, so pick it.
- **No terminal means no prompts.** When stdin is not a terminal, ragalay refuses every question it would ask (exit code 4) instead of assuming yes. You answer with flags (`--yes`, `--accept-license`), and only after the user has said yes to that specific thing.
- **Long commands go in the background.** `setup` and the first `scan` can run for many minutes. Start them in the background and poll the log or `status --json` instead of blocking.
- **One indexer at a time.** While the ragalay window is open, it holds `.ragalay/index.lock` and indexes on its own. A `scan` from you then exits 5. That is fine: poll `status --json` and let the window finish.
- **Paths:** `ROOT` below is the user's documents folder (absolute path) and `RG` is the path to the program (`ragalay`, or `ragalay.exe` on Windows). Pass `--root "$ROOT"` on every command, or run from inside `ROOT`.

## 1. Ask the user

Ask these together, in plain words:

1. **Which folder** holds the documents to search? Should everything inside it be searchable, or only some subfolders?
2. **Which model?** Recommend the default.
   - **EmbeddingGemma 2** (default): free for any use, including work. About 2.5–6 GB one-time download, depending on the computer.
   - **Jina v5**: slightly better on benchmarks, larger (about 10 GB disk), **personal and research use only** (CC BY-NC 4.0).
   - **Another service**: an embedding model they already run (Ollama, LM Studio) or an online API. Nothing is downloaded. Online services receive the documents, so ask for the address, the model name, and (for online) the *name* of the environment variable that holds the API key.
3. Is the **one-time download** OK? You'll give the exact size in step 4.

**Done when** you know the folder, the subfolders (or "everything"), the model, and whether the user accepts the download.

## 2. Download the program

Pick the archive for the computer:

| OS / CPU | Archive |
|---|---|
| Windows x64 | `ragalay_windows_amd64.zip` |
| macOS Apple silicon (`uname -m` = `arm64`) | `ragalay_darwin_arm64.tar.gz` |
| macOS Intel (`x86_64`) | `ragalay_darwin_amd64.tar.gz` |
| Linux x86_64 / aarch64 | `ragalay_linux_amd64.tar.gz` / `ragalay_linux_arm64.tar.gz` |

Windows on ARM has no build. There, tell the user and stop.

Download the archive and `checksums.txt` from the latest release, check the SHA-256, and unpack it into `ROOT`. The program goes **inside the documents folder** so the user can double-click it later.

macOS / Linux:

```sh
A=ragalay_darwin_arm64.tar.gz   # from the table
B=https://github.com/satlavida/ragalay/releases/latest/download
cd "$ROOT"
curl -fLO "$B/$A" && curl -fLO "$B/checksums.txt"
grep " $A\$" checksums.txt | shasum -a 256 -c -     # Linux: sha256sum -c -
tar -xzf "$A" ragalay && rm "$A" checksums.txt
./ragalay version
```

Windows (PowerShell):

```powershell
$A = "ragalay_windows_amd64.zip"
$B = "https://github.com/satlavida/ragalay/releases/latest/download"
Set-Location $ROOT
Invoke-WebRequest "$B/$A" -OutFile $A; Invoke-WebRequest "$B/checksums.txt" -OutFile checksums.txt
$want = (Select-String -Path checksums.txt -Pattern " $A$").Line.Split(" ")[0]
if ((Get-FileHash $A -Algorithm SHA256).Hash -ne $want.ToUpper()) { throw "checksum mismatch" }
Expand-Archive $A -DestinationPath $env:TEMP\ragalay-unpack -Force
Copy-Item $env:TEMP\ragalay-unpack\ragalay.exe .; Remove-Item $A, checksums.txt
.\ragalay.exe version
```

If `releases/latest` returns 404, only pre-releases exist (or the repository needs sign-in). Use the GitHub CLI with an explicit tag: `gh release list -R satlavida/ragalay`, then `gh release download <tag> -R satlavida/ragalay -p <archive> -p checksums.txt`. If that fails too, ask the user how they got access to ragalay.

Files fetched with `curl` / `Invoke-WebRequest` don't carry the "downloaded from the internet" mark, so the system warning the README describes won't appear. If the *user* downloaded it in a browser, clear the mark with `xattr -d com.apple.quarantine ragalay` (macOS) or `Unblock-File ragalay.exe` (Windows), or follow the README's click-through steps.

**Done when** `RG version` prints a version and the program sits in `ROOT`.

## 3. Make the folder searchable

```sh
RG init "$ROOT"
RG folders add "$ROOT/<subfolder>" --root "$ROOT"   # once per subfolder; skip for "everything"
RG folders list --json --root "$ROOT"
```

Subfolders must be inside `ROOT`. Give them as absolute paths: a relative path is resolved against the current directory, not `ROOT`. For a model other than the default:

```sh
RG model use jina-v5 --no-index --root "$ROOT"
RG model use openai --base-url http://localhost:11434/v1 --model nomic-embed-text --no-index --root "$ROOT"
RG model use openai --base-url https://api.openai.com/v1 --model text-embedding-3-small \
    --api-key-env OPENAI_API_KEY --no-index --root "$ROOT"
```

`model use jina-v5` saves the setting, then stops with exit 4 at the license and download question. That is expected: step 4 installs it. For a service, `model use` first checks that it answers and changes nothing if it doesn't. `RG model test --root "$ROOT"` repeats the check. API keys: the user puts the key in the named environment variable themselves. Never write a key into the folder, the config, or a file. On Windows, the ragalay window can store it (Model view, Ctrl+K).

**Done when** `RG status --json` shows the chosen folders under `folders` and the chosen model under `embed`.

## 4. Install the model (local models only)

Skip this step for a service: `setup` would only say there is nothing to install.

First run `setup` **without** `--yes`. With no terminal, it prints the license note, the accelerator it detected, and every download with its size, then stops with exit 4 and downloads nothing:

```sh
RG setup --root "$ROOT"
```

Tell the user the total size and the accelerator. For Jina v5, also show the license text and get an explicit yes. Then start the real install in the background:

```sh
RG setup --yes --root "$ROOT"                     # Jina v5: add --accept-license
```

Follow progress in `ROOT/.ragalay/logs/setup.log`. If it stops (network drop, sleep), run the same command again: finished steps are skipped and downloads resume. Downloads go to a shared per-user folder (`%LOCALAPPDATA%\ragalay`, `~/Library/Caches/ragalay`, `~/.cache/ragalay`), so another folder on this computer won't download them again.

`--device auto` picks NVIDIA (CUDA), Apple (MPS), AMD RX 9070/9060 on Windows (ROCm) or the CPU. Leave it on auto unless the log shows a wrong guess.

**Done when** the output ends with `Setup complete.` and `RG status --json` shows `"setup": {"ready": true, ...}`.

## 5. First index

```sh
RG scan --json --root "$ROOT"                     # background; a service on another computer: add --yes, see below
```

Poll `RG status --json --root "$ROOT"` until `queued` is 0, `index.by_status` has no `pending` or `processing`, and `indexing` is absent. A document's chunks are written when the whole file finishes, so a big PDF shows `processing` with 0 chunks for a while. That is normal. Speed: with a GPU, about 0.1 s per PDF page. On the CPU, about 0.3 s per passage and 5 s per PDF page. Searching works while indexing runs.

On a CPU-only computer with many PDFs, offer to skip page pictures (much faster, but scanned PDFs become unsearchable): set `pdf_page_images = false` under `[index]` in `ROOT/.ragalay/config.toml` before scanning.

**Sending documents away:** if the model is a service that isn't on this computer (anything other than `localhost` / `127.0.0.1`), `scan` stops with an error that says how much would be sent where. Show that message to the user. Add `--yes` only after they agree.

Then list failures: `RG docs --status failed --json --root "$ROOT"`. Each one has an `error` field. Tell the user which files failed and why.

**Done when** indexing is idle, `index.by_status.done` matches the documents you expect, and every failed file has been reported to the user.

## 6. Check that search works

Pick a topic you saw in the user's file names and search for it:

```sh
RG search "<a question about one of their documents>" -k 3 --json --root "$ROOT"
```

**Done when** a relevant result comes back (exit 0) and you've shown the user one hit (file, page, text).

## 7. Connect ragalay to the agent

Ask which agent(s) the user wants to search with. For Claude Code, add it for the user's whole account so it works in any project. Use absolute paths:

```sh
claude mcp add -s user ragalay -- "<abs path to RG>" mcp --root "<ROOT>"
```

Claude Desktop and other MCP clients: see "MCP" in [README.md](README.md). For several document folders, register one server per folder with distinct names (`ragalay-work`, `ragalay-papers`).

Without MCP, an agent can call the CLI. Offer to add this to the `CLAUDE.md` / `AGENTS.md` of the projects where the user wants it:

```markdown
## Searching my documents
My documents in <ROOT> are indexed by ragalay. Before answering from my documents, search them:
`<RG> search "<query>" --json --root "<ROOT>"` (add `--mode keyword` for exact names, `--group-by doc` to see which files match).
Cite results by `path` and `page`. `RG status --json` shows what is indexed.
```

**Done when** the user's agent lists the ragalay tools (`claude mcp list` shows `ragalay` connected) or the snippet is in the file the user chose.

## 8. Show the user around

Keep it short, in plain words, and point to [README.md](README.md) for the rest:

- **Double-click** `ragalay` in the folder to open the search window. Type a question, press Enter, use ↑/↓ to pick a result and Enter to open it. Tab switches between Search, Status, Folders and Model. `q` quits.
- **New files** are picked up while the window is open, or the next time it opens (or when an agent runs `scan`).
- **Ask the agent**: "search my documents for …" now works through the connection from step 7.
- **Updating**: `ragalay update`. **Freeing space**: `ragalay setup --prune`.

**Done when** the user knows how to open the window and how to ask you to search.

## Using ragalay as an agent (reference)

- **Search tactics:** the default `hybrid` mode suits questions. Use `--mode keyword` for exact names, codes and numbers, and `--group-by doc` to find *which files* cover a topic. Narrow with `--path-glob "folder/*"` or `--modality text,image,pdf_page`. Run several short, specific searches for different parts of a question rather than one long one.
- **Results:** `--json` prints an array, best first. Fields that are empty are left out. Cite `path` and `page`. A `pdf_page` hit matched the picture of a page and has no `text`, so open the page if you need the words. `paired_path` is a Markdown transcription of the PDF (or the reverse), usually easier to read. `parent_path` is the Markdown file a picture appears in. Scores only rank results.
- **Notices:** if the index is mid-way through a model switch, `search` still answers and prints `note: …` on stderr (MCP: the `notice` field).
- **Exit codes:** 0 OK, 1 error, 2 run `RG reembed`, 3 run `RG init`, 4 setup incomplete or a setup question went unanswered, 5 another ragalay is indexing (wait, then retry). Details in [README.md](README.md).
- **Problems:** read `ROOT/.ragalay/logs/` (`setup.log`, `index.log`, `sidecar.log`) and `RG status --json` before guessing. The README's "Something not working?" table covers the common cases.
