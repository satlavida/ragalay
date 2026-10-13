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
- **Decision (user, 2026-10-13):** text chunks split on blank lines, as whole lines. No tree-sitter, no CGO. The text model embeds them, and BM25 indexes identifiers (split camelCase and snake_case as extra tokens).
- Skips: files over a size cap (default 1 MB), minified or generated files (long lines, `node_modules`, `vendor`, lockfiles), binary content.
- **Built (2026-10-13, Phase A):** the line range is stored in `heading_path` as `main.go:40-88`, so no schema change or shadow-table migration was needed, and results, MCP and the TUI already show it. Dedicated `start_line`/`end_line` columns can wait until something needs to filter on them. camelCase splitting in BM25 is deferred: it changes the shared tokenizer and would force a reindex of every folder.
- Opening a hit at its line in an editor is not done yet.
- Needs no new model, so it can ship before audio and video.

## Phase 0 findings (2026-10-13, Gemma 2 only, Apple M3 Pro, 5 CPU threads / MPS)

Scratch scripts were run from the session scratchpad, not committed. Jina was **not** tested: it is a 4.9 GB download under a non-commercial license, so it needs the user's go-ahead.

| Q | Finding |
|---|---|
| Q1 max clip | **Audio is silently cut at 30 s** (750 tokens). The model card says ~327 s, but the processor drops the rest: 60 s and 120 s clips still give 750 tokens. Windows must be **≤ 30 s** (keep the 30 s / 5 s overlap default). Video: 140 tokens/frame, default cap 32 frames at 1 fps, so a window is **≤ 32 s**. |
| Q2 speed | Audio, 30 s window: ~1.2 s on CPU and ~1 s on MPS, so an hour of audio is ~2.5 min. Video, per frame: ~0.6 s on CPU, ~0.3 s on MPS (240p frames), so an hour of video at 1 fps is ~36 min on CPU and ~18 min on MPS. Audio is cheap on every device; video is the cost. Video must be scheduled after everything else, and its cost unit in Plan 6's model is frames. |
| Q4 decoding | PyAV 18.1 installs and decodes H.264 on macOS arm64. Windows and Linux are untested. The model library asks for `torchcodec` for video paths, which can be avoided: pass frames decoded by PyAV as a numpy array (worked). |
| Q5 profiles | Gemma 2: yes to audio and video (above). Jina: untested. `openai` profile stays text and image only. |
| Deps | Audio input through sentence-transformers needs `librosa` (pulls numba, scipy, scikit-learn); `av`, `soundfile`, `librosa` and their dependencies are ~320 MB extra in the Python environment. To check in the build phase: pass a decoded numpy array for audio and drop `librosa`. Setup must install these only when a folder enables audio or video. |
| Sidecar | `sidecar.py` loads Gemma with `audio_config: None`; media needs the audio encoder (+570M parameters in memory) and the vision encoder loaded together. Loading it only when a media job exists keeps text indexing light. |

**Q3 decided (user, 2026-10-13): yes.** A video is indexed as two streams: its frames in ≤ 32 s windows, and its audio track extracted by PyAV and run through the audio path in ≤ 30 s windows (~1 s each). Both kinds of chunk carry the same `start_ms`/`end_ms` and belong to the one video document, so a spoken phrase finds the right moment even when the picture does not show it. A video without an audio track indexes frames only. Search merges hits from the two streams that overlap in time into one result. Not yet tested: extracting audio from a video with PyAV and resampling to 16 kHz mono.

Open still: Jina results, Windows and Linux decoding.

## Schema changes
- `documents.kind` gains `code` (no schema change; the column is free text).
- `chunks` gains `start_ms`, `end_ms`. `documents.kind` gains `audio | video`. `modality` gains `audio | video`.

## Open questions
1. What's omni-small's practical maximum audio and video clip length? That sets the default window size.
2. CPU throughput for audio and video on the Windows PC: is it acceptable, or should media need a GPU or opt-in?
3. Should video also embed its audio track separately?
4. Do PyAV wheels decode common formats on all 3 OSes without a system FFmpeg?
5. Plan 2 makes EmbeddingGemma 2 the default. It has its own audio encoder (25 tokens/s) and video sampling (1 fps), so Q1–Q3 need answers per profile (Gemma, Jina), and external providers stay text/image-only.

## Phases (draft)
- Phase 0 (Gemma 2 half done 2026-10-13, see findings): spikes on Q1–Q5 (user chose this over a transcription-only design; if native audio/video is too slow or unsupported on a profile, fall back to Whisper transcripts indexed as timestamped text for that profile)
- Phase A: code files ✅ Done (2026-10-13), shipped ahead of the media phases
- Phase 1: audio windowing + ingest
- Phase 2: video windowing + ingest
- Phase 3: search by example (CLI, MCP, TUI)
- Phase 4: MD page markers and pairing merge
- Phase 5: docs and release
