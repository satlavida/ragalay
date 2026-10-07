# Phase 0 spikes

Throwaway experiments behind the decisions in `plans/plan1/plan.md` §3.1 and `plans/archive/plan2/plan.md` §3.1. None of this code ships. Re-run them on new hardware (e.g. the M3 Pro) and record the numbers in the plan.

| Dir | What it checks | Run |
|---|---|---|
| `turso/` | tursogo vectors, ANN, FTS, cross-process access | `go run ./spikes/turso [-exp index_method] <db>` |
| `tursolock/` | in-process connection pool, open/close cycle cost | `go run ./spikes/tursolock <db from turso spike>` |
| `pdftext/` | ledongthuc/pdf vs go-pdfium (wasm) text quality | `go run ./spikes/pdftext spikes/fixtures/*.pdf` |
| `bm25/` | BM25 tables in Turso, scored in Go | `go run ./spikes/bm25 <db> spikes/fixtures/attention.pdf spikes/fixtures/bert.pdf` |
| `omni/` | omni-small load, throughput per modality, RAM, combined input; dumps query vectors | `python -I spikes/omni/omni_spike.py spikes/fixtures spikes/omni/out <cpu\|cuda\|mps>` |
| `chunkeval/` | chunk sizes 256/512/1024, whole page, page image: hit@1, hit@5, MRR | `python -I spikes/chunkeval/chunkeval.py spikes/fixtures <cpu\|cuda\|mps>` |
| `mcpclient/` | drives `ragalay mcp` over stdio with the official MCP client | `go run ./mcpclient <ragalay binary> <root>` |
| `llama/` | text-small GGUF via yzma and via llama-server, parity with omni | `go run ./spikes/llama -lib <llama.cpp dir> -model <gguf> -omni spikes/omni/out/omni_queries.json` |
| `gemma/gemma_spike.py` | EmbeddingGemma 2 load, speed per modality, image input forms, text-only loading; dumps query vectors | `python spikes/gemma/gemma_spike.py spikes/fixtures spikes/gemma/out <cpu\|cuda>` (transformers >= 5.19) |
| `gemma/llama/` | Gemma 2 GGUF via yzma, parity with the full model | `go run ./gemma/llama -lib <llama.cpp b11459+> -ref gemma/out/gemma_queries_cpu.json -model <gguf>` |
| `gemma/eval.py` | retrieval: Gemma 2 vs Jina v5 (dims, title prompt, page images) | `python spikes/gemma/eval.py spikes/fixtures <cpu\|cuda>` |
| `openaicompat/` | how an OpenAI-compatible `/v1/embeddings` server behaves (base64, dimensions, batching, limits, image shapes) | `go run ./openaicompat -base <url>/v1 -model <name> [-image fixtures/car.jpg]` |

## Setup
- Python: `uv venv --python 3.11` then `uv pip install torch torchvision transformers sentence-transformers pillow pypdfium2 numpy psutil` (pick the torch index for your device).
- llama.cpp: `go install github.com/hybridgroup/yzma@latest && yzma install -l <dir> -p <vulkan|metal|cuda|cpu>`.
- GGUF: `https://huggingface.co/jinaai/jina-embeddings-v5-text-small-retrieval-GGUF` (`v5-small-retrieval-Q8_0.gguf`, `-F16.gguf`).

## Fixtures
`fixtures/` holds public test files: the arXiv papers 1706.03762 (Attention) and 1810.04805 (BERT), a Hugging Face sample image, and `scanned.pdf`, an image-only PDF rendered from two Attention pages. They're downloaded, not committed (see `.gitignore`).
