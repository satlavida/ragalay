# Plan 3: audio, video, code files, and search by example

**Status:** Draft (Plan 2 is archived. Scope confirmed 2026-10-13: spike first, then build; finalize after Phase 0 answers Q1–Q5)
**Created:** 2026-10-06
**Depends on:** Plan 1 (omni sidecar, job queue, schema, search), Plan 2 (embedding profiles: media needs a profile whose Modalities include audio/video)

## Scope
- **Audio** ingestion (wav/mp3/flac/ogg/m4a…): fixed windows (default 30 s, 5 s overlap), `start_ms`/`end_ms` on chunks, decoding via `librosa`/`soundfile`.
- **Video** ingestion (mp4/mov/mkv/avi/webm…): windows with frame sampling by the omni processor, decoding via PyAV (`av`) wheels, which bundle FFmpeg.
- **Search by example:** `ragalay search --image photo.jpg` / `--audio clip.wav`. This needs the Python sidecar, unlike text search.
- Results and TUI: `mm:ss` time ranges. Opening a file at its timestamp where the player supports it.
- **Pairing polish:** MD page markers (`<!-- page 3 -->`, `## Page 3`, form feed) give MD chunks page numbers, so they merge exactly with PDF page hits.
- ETA tuned for media: duration is the cost unit in Plan 6's cost model; media scheduled after text.

## Code files (added 2026-10-13, user request)
- **New config kind `code`** (opt-in via `scan.kinds`, on by default for new folders). Common extensions: go, py, js, ts, tsx, jsx, rs, java, kt, c, h, cc, cpp, hpp, cs, rb, php, swift, sh, sql, lua, toml, yaml, json (size-capped).
- **Decision (user, 2026-10-13):** text chunks split on blank lines and top-level block boundaries, with `start_line`/`end_line` on chunks. No tree-sitter, no CGO. The text model embeds them, and BM25 indexes identifiers (split camelCase and snake_case as extra tokens).
- Skips: files over a size cap (default 1 MB), minified or generated files (long lines, `node_modules`, `vendor`, lockfiles), binary content.
- Results and TUI show `path:start-end`. Opening a hit jumps to the line where the editor supports it.
- Needs no new model, so it can ship before audio and video.

## Schema changes
- `chunks` gains `start_ms`, `end_ms`, `start_line`, `end_line`. `documents.kind` gains `code`.
- `chunks` gains `start_ms`, `end_ms`. `documents.kind` gains `audio | video`. `modality` gains `audio | video`.

## Open questions
1. What's omni-small's practical maximum audio and video clip length? That sets the default window size.
2. CPU throughput for audio and video on the Windows PC: is it acceptable, or should media need a GPU or opt-in?
3. Should video also embed its audio track separately?
4. Do PyAV wheels decode common formats on all 3 OSes without a system FFmpeg?
5. Plan 2 makes EmbeddingGemma 2 the default. It has its own audio encoder (25 tokens/s) and video sampling (1 fps), so Q1–Q3 need answers per profile (Gemma, Jina), and external providers stay text/image-only.

## Phases (draft)
- Phase 0: spikes on Q1–Q5 (user chose this over a transcription-only design; if native audio/video is too slow or unsupported on a profile, fall back to Whisper transcripts indexed as timestamped text for that profile)
- Phase A: code files (independent of the spikes, can ship first)
- Phase 1: audio windowing + ingest
- Phase 2: video windowing + ingest
- Phase 3: search by example (CLI, MCP, TUI)
- Phase 4: MD page markers and pairing merge
- Phase 5: docs and release
