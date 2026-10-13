# Plan 3: audio, video, and code files

**Status:** Completed (2026-10-13). Verified only on macOS arm64 with EmbeddingGemma 2; see "Not verified" below.
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

### Phase 1: Foundations (schema, kinds, profile) ✅ Completed (2026-10-13)
**Goal:** The index and scanner know about audio and video; nothing is embedded yet.
- [x] Schema v4 migration with `start_ms`, `end_ms`; shadow DDL and store tests (fresh DB and upgrade from v3).
- [x] `ChunkRow` and `ReplaceChunks` carry the times; `embed.Audio` and `embed.Video` modalities; `embed.Input` gets `StartMS`, `EndMS`; Gemma 2 profile lists both.
- [x] Scan kinds `audio`, `video`, extension tables, config `supportedKinds` and defaults; job order (D7).
- [x] Runner: a media job on a profile without the modality finishes with a note, not a failure.
- [x] Plan 6 cost: media jobs cost seconds (size-based guess until probed); default rates.

**Exit:** `go test -short ./...` passes; an upgraded v3 index opens; a media file on the Jina or `openai` profile is marked done with the note.

### Phase 2: Sidecar decoding and embedding ✅ Completed (2026-10-13)
**Goal:** The Python helper probes media and embeds windows.
- [x] `sidecar.py`: `probe`; audio and video inputs with `start_ms`/`end_ms`; load the audio and vision encoders for Gemma on demand; `av` and `numpy` only.
- [x] Extraction of a video's audio track (resample to 16 kHz mono), and of video frames at 1 fps.
- [x] `setup` installs `av` (pinned in `pins.go`, so the package hash changes and the next `setup` installs it); a missing `av` surfaces the Python ImportError from the sidecar.
- [x] Go client: `Probe`, media inputs. Fake-sidecar protocol test for `probe` and media inputs. The real-model check ran as scratch scripts (generated 70 s wav, 40 s mp4 with AAC audio, 20 s silent mp4); a committed real-model test comes with Phase 3.

**Exit:** met on this Mac: probe reports duration and streams; a 30 s window of a wav, the audio track of an mp4 and 30 frames of the mp4 each give a 768-dim vector; text vectors are unchanged after the audio encoder loads (cosine 1.0); a 30-frame window takes ~9 s on MPS. (Window planning is Phase 3.)

### Phase 3: Windowing and indexing ✅ Completed (2026-10-13)
**Goal:** Media files are indexed end to end.
- [x] `internal/extract/media.go`: windows (D2) from the probed duration; chunk text empty, `heading_path` `mm:ss–mm:ss`.
- [x] Runner: start the embedder before extracting a media job, probe, then embed in batches with Plan 6 progress.
- [x] Failures (corrupt file, no streams) are per-document errors with clear text.
- [x] Tests with the fake embedder; real-model audio test in `internal/setup/integration_test.go` (skipped without the profile or with `-short`). Video windows were checked by hand with scratch scripts, since a Go test cannot make an mp4.

**Exit:** met on this Mac with the real model: a folder with two spoken-word recordings (macOS `say`, m4a), an mp4 and a note indexed in 30 s (6 chunks); the query "how do I bake banana bread" returns the banana-bread recording at rank 1, "quarterly budget meeting lighthouse renovation" returns the budget recording at rank 1, and "gardening tomatoes" returns the note.

### Phase 4: Search output and docs ✅ Completed (2026-10-13)
**Goal:** People and agents see time ranges.
- [x] `search` JSON gains `start_ms`/`end_ms`; overlapping windows of one file merge (D9), widening to at most two windows so a long video does not collapse into one span; a merged frame+sound hit has modality `audio+video`.
- [x] CLI, TUI and MCP show `mm:ss–mm:ss`.
- [x] README and SETUP-FOR-AGENTS: what is read, speed expectations (about 2.5 min per hour of audio, 18–36 min per hour of video), the 30 s window.
- [x] `go test -short ./...` passes. Release notes are written at release time (Plan 2's `release-notes.md` pattern), not here.

**Exit:** documented, tested, committed; plan archived and tagged `plan3`.

## 5. Not verified at completion
- **Windows and Linux:** PyAV decoding, CPU speed and the setup install of `av` were not run there. The code uses `filepath` and PyAV only, so nothing is OS-specific, but no one has tried it.
- **Jina v5:** untested for media, so its profile does not list `audio` or `video`. Such files are skipped with a note.
- **Long files:** the largest media file indexed was 70 s of generated audio and a 40 s video. Hour-long files rely on the measured per-window speed, not a run.
- **Video content search:** frame embeddings were checked for shape and speed, not for retrieval quality (the test video was random noise). Audio retrieval was checked with real speech.
- **Real-model video test:** none is committed (a Go test cannot create an mp4); the video path is covered by the fake-embedder runner test and the manual run.

## 6. Risks
- Video speed on CPU-only machines: scheduled last, and estimates say so (Plan 6). A per-folder `kinds` setting turns video off.
- The 30 s audio cut is a model-processor behavior and could change in a model revision; the pinned revision protects us, and a test asserts the token count.
- Windows and Linux decoding are untested; Phase 2 adds their results if the user runs the scripts there.
