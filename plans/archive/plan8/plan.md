# Plan 8: Search confidence

**Status:** Completed (2026-10-13)

## Problem
Search always returned the top K hits. The `score` is a reciprocal-rank-fusion value (about 0.033 for any query), so it says nothing about quality: a query for something not in the folder looked exactly like a good one.

## Measurements (EmbeddingGemma 2, 117-chunk folder of Markdown, code, speech audio and a video)
| Queries | Best cosine similarity |
|---|---|
| Real matches (8) | 0.69 to 0.88 |
| Nonsense queries (5) | 0.56 to 0.62, but gibberish ("asdf qwerty zxcv") hit the test video at 0.69 |

Audio and video score higher than text on unrelated queries. A z-score against the other chunks was a poor separator (nonsense 1.9 to 3.8, real 2.3 to 6.9), so the thresholds are absolute and per profile.

## Decisions
- Each hit carries `similarity` (best cosine, kept through merges) and `confidence`: high at 0.75 and up, medium at 0.65 and up, low below (`Profile.StrongSim/FairSim`). A passage that contains at least half of the query's words moves up one level.
- Default (`--confidence auto`): drop low hits when a medium or high one exists; next to a clear winner (high) drop hits more than 0.10 below it. If nothing is better than low, show the closest 3 with a note. `all`, `medium`, `high` override.
- Profiles with no thresholds (Jina, openai) and keyword-only searches report no confidence and hide nothing.
- Surfaced in the CLI, `--json`, the TUI (label next to the path) and the MCP tool (description tells agents to trust only high and medium).
- `start_ms` is now always present when `end_ms` is (it was dropped at 0).

## Tasks
- [x] Measure similarity on relevant and unrelated queries
- [x] `Similarity`/`Confidence` on results, rating and filtering (`internal/search/confidence.go`), tests
- [x] CLI flag and output, TUI label, MCP argument, README

## Not verified
- Jina and OpenAI-compatible profiles are uncalibrated.
- Gibberish queries can still reach medium through video frames (0.69).
- Thresholds come from one small English corpus; other languages and large folders may shift them.
