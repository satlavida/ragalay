# Plan 7: search by example, and Markdown page markers

**Status:** Draft (split out of Plan 3 on 2026-10-13; read and refine before finalizing)
**Created:** 2026-10-13
**Depends on:** Plan 3 (audio and video chunks, `start_ms`/`end_ms`)

## Candidate scope
- **Search by example:** `ragalay search --image photo.jpg` / `--audio clip.wav` / `--video clip.mp4`. Needs the Python sidecar, unlike text search. CLI, MCP and TUI.
- **Open at the timestamp:** open a hit in the player at its `start_ms`, where the player supports it (VLC, mpv, QuickTime has no flag).
- **Pairing polish:** MD page markers (`<!-- page 3 -->`, `## Page 3`, form feed) give MD chunks page numbers, so they merge exactly with PDF page hits.
- Code files: a hit opens at its line in an editor; camelCase splitting in BM25 (needs a reindex; see Plan 3 Phase A notes).

## Open questions
1. Which matters first after using audio and video for a while?
2. Should a search-by-example query go through the running TUI sidecar, or start its own?
