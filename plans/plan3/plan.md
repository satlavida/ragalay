# Plan 3: audio, video, and code files

**Status:** In progress (finalized 2026-10-13, after the user chose spike-first, native embedding, and "index a video's audio track separately")
**Created:** 2026-10-06 · **Finalized:** 2026-10-13
**Depends on:** Plan 1 (sidecar, job queue, schema, search), Plan 2 (embedding profiles), Plan 6 (cost model for estimates)
**Moved out:** search by example, MD page markers, open-at-timestamp → `plans/plan7` (not part of the 2026-10-13 request)

## 1. Scope
- **Code files** (Phase A, done): text passages with line ranges.
- **Audio** (wav, mp3, flac, ogg, m4a, aac, opus, wma): fixed windows, `start_ms`/`end_ms` on chunks.
- **Video** (mp4, mov, mkv, avi, webm, m4v): frame windows, plus the audio track indexed separately through the audio path (user decision, 2026-10-13).
- Results show `mm:ss` ranges. Estimates use media duration as the cost unit (Plan 6).

## 2. Decisions

| # | Decision |
|---|---|
| D1 | Native embedding by the profile's own audio and video towers. Only profiles listing `audio` / `video` in `Modalities` index media: **EmbeddingGemma 2 now**. Jina is untested (not downloaded, non-commercial license); `openai` stays text and image. On any other profile a media file finishes with a note ("this model cannot read audio; switch to EmbeddingGemma 2") and no chunks; it is not a failure and is retried when the profile changes. |
| D2 | Windows: **audio 30 s with 5 s overlap** (the model's processor cuts audio at 30 s, 750 tokens, silently); **video 30 frames at 1 fps (30 s) with 5 s overlap**. Both are config-free constants in this plan. |
| D3 | Python does all decoding, with PyAV (`av`) and `numpy`. The sidecar gets a `probe` call (duration, has_audio, has_video) and windows are passed as `{path, start_ms, end_ms}`. Audio is resampled to 16 kHz mono and passed as a numpy array. This needs neither `librosa` nor `torchcodec` (both verified unnecessary in the Phase 0 spikes). Video frames are decoded with PyAV and passed as a numpy array. |
| D4 | `av` is installed by `ragalay setup` for local profiles (small wheel, bundles FFmpeg). Existing installs get it on the next `setup` or on first media job, with a clear error pointing at `ragalay setup` if missing. |
| D5 | Schema v4: `chunks` gains `start_ms` and `end_ms` (nullable). Shadow tables are created with the same DDL. `documents.kind` gains `audio | video`; `modality` gains `audio | video` (both free text). |
| D6 | A video's chunks: frames (`modality=video`) and audio-track windows (`modality=audio`), same document. A video with no audio track indexes frames only. |
| D7 | Scheduling: text, code, PDFs, images, then audio, then video (video is the slowest). |
| D8 | Media files default **on** for new folders (`kinds` includes `audio`, `video`). Existing folders keep their list. Nothing runs until a media file exists. |
| D9 | Search result JSON gains `start_ms` and `end_ms` (omitted when absent). Video frame hits and audio-track hits of one video that overlap in time merge into one result. |
| D10 | Plan 6's cost unit for media is **seconds of duration**, learned per kind; duration is known after `probe`, so media jobs get their cost refined before the first window is embedded. |

## 3. Phase 0 findings (2026-10-13, Gemma 2 only, Apple M3 Pro, 5 CPU threads / MPS)

Scratch scripts ran from the session scratchpad and are not committed. Jina was not tested.

| Q | Finding |
|---|---|
| Max clip | **Audio is silently cut at 30 s** (750 tokens). The model card says ~327 s, but the processor drops the rest. Video: 140 tokens/frame, default cap 32 frames at 1 fps. |
| Speed | Audio, 30 s window: ~1.2 s on CPU and ~1 s on MPS, so an hour is ~2.5 min. Video, per frame: ~0.6 s on CPU, ~0.3 s on MPS (240p), so an hour at 1 fps is ~36 min on CPU, ~18 min on MPS. |
| Decoding | PyAV 18.1 decodes H.264 on macOS arm64; Windows and Linux untested. PyAV-decoded numpy arrays work for both audio and video. Audio as a `torch` tensor is rejected; it must be numpy. |
| Deps | Without `librosa`, a numpy array for audio works (verified). `av` is the only addition. |
| Sidecar | `sidecar.py` loads Gemma with `audio_config: None`. Media needs the audio and vision encoders; load them only when the first media job arrives (a second model load in the same process, or restart the process with them enabled). |

Not yet checked: Windows and Linux decoding; Jina; audio extraction from a video container (Phase 2 tests it).

## 4. Phases

### Phase A: Code files ✅ Completed (2026-10-13)
- [x] Config kind `code`, extension table, size cap (1 MB), skip minified and lockfiles, default ignores.
- [x] Line-range passages labelled `file.go:40-88` (stored in `heading_path`, so no schema change).
- [x] Scan and extract tests; README.

Notes: line ranges live in `heading_path`. camelCase BM25 splitting and open-at-line move to Plan 7.

### Phase 1: Foundations (schema, kinds, profile)
**Goal:** The index and scanner know about audio and video; nothing is embedded yet.
- [ ] Schema v4 migration with `start_ms`, `end_ms`; shadow DDL and store tests (fresh DB and upgrade from v3).
- [ ] `ChunkRow` and `ReplaceChunks` carry the times; `embed.Audio` and `embed.Video` modalities; `embed.Input` gets `StartMS`, `EndMS`; Gemma 2 profile lists both.
- [ ] Scan kinds `audio`, `video`, extension tables, config `supportedKinds` and defaults; job order (D7).
- [ ] Runner: a media job on a profile without the modality finishes with a note, not a failure.
- [ ] Plan 6 cost: media jobs cost seconds (size-based guess until probed); default rates.

**Exit:** `go test ./...` passes; an upgraded v3 index opens; a media file on the Jina or `openai` profile is marked done with the note.

### Phase 2: Sidecar decoding and embedding
**Goal:** The Python helper probes media and embeds windows.
- [ ] `sidecar.py`: `probe`; audio and video inputs with `start_ms`/`end_ms`; load the audio and vision encoders for Gemma on demand; `av` and `numpy` only.
- [ ] Extraction of a video's audio track (resample to 16 kHz mono), and of video frames at 1 fps.
- [ ] `setup` installs `av`; clear error when missing.
- [ ] Go client: `Probe`, media inputs. Python tests with generated wav and mp4 (skipped when the profile is not installed).

**Exit:** a generated 70 s wav gives 3 windows with vectors of the right size; a generated mp4 gives frame and audio vectors; verified on this Mac.

### Phase 3: Windowing and indexing
**Goal:** Media files are indexed end to end.
- [ ] `internal/extract/media.go`: windows (D2) from the probed duration; chunk text empty, `heading_path` `mm:ss–mm:ss`.
- [ ] Runner: start the embedder before extracting a media job, probe, then embed in batches with Plan 6 progress.
- [ ] Failures (corrupt file, no streams) are per-document errors with clear text.
- [ ] Tests with the fake embedder; one real-model test (skipped without the profile).

**Exit:** a folder with a wav, an mp4 and text indexes; `search` finds the audio window by a text query matching what is said or heard (checked by hand on a real clip).

### Phase 4: Search output and docs
**Goal:** People and agents see time ranges.
- [ ] `search` JSON gains `start_ms`/`end_ms`; overlapping frame and audio hits of one video merge (D9).
- [ ] CLI, TUI and MCP show `mm:ss–mm:ss`.
- [ ] README and SETUP-FOR-AGENTS: what is read, speed expectations (about 2.5 min per hour of audio, 18–36 min per hour of video), the 30 s window.
- [ ] Release notes; `go test ./...` passes.

**Exit:** documented, tested, committed; plan archived and tagged `plan3`.

## 5. Risks
- Video speed on CPU-only machines: scheduled last, and estimates say so (Plan 6). A per-folder `kinds` setting turns video off.
- The 30 s audio cut is a model-processor behavior and could change in a model revision; the pinned revision protects us, and a test asserts the token count.
- Windows and Linux decoding are untested; Phase 2 adds their results if the user runs the scripts there.
