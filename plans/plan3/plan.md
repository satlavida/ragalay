# Plan 3: speed, background indexing, distribution

**Status:** Draft (starts after Plan 2 is archived; read and refine before finalizing)
**Created:** 2026-10-06

## Candidate scope
- **Fast text indexing:** embed text chunks with the text-small GGUF through llama.cpp instead of Python omni. The vectors are the same, so no re-embed is needed, and Python is then required only for images and media. Depends on the Plan 1 parity results.
- **Background indexing (optional):** a "keep indexing when the app is closed" setting using a per-OS login item or service (launchd, Windows Task Scheduler/Service, systemd user unit).
- **Distribution:** Homebrew tap, winget manifest, macOS signing and notarization, Windows code signing.
- **OCR text** for scanned PDF pages (snippets + keyword search), if page-image retrieval proves insufficient.
- **TUI image previews** (kitty/sixel/iTerm protocols).
- Other providers (vLLM on CUDA, remote Jina API, Ollama) behind the existing `Embedder` interface.

## Open questions
1. Which of these matter most after daily use of Plans 1–2?
2. Is a paid Apple developer account worth it for notarization?
