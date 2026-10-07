"""Plan 2 Phase 0 spike: EmbeddingGemma 2 through sentence-transformers.

Checks load time and memory, throughput per modality, which image input forms
work, whether text-only loading changes text vectors, and dumps query vectors
for the GGUF parity check (spikes/gemma/llama).

Needs transformers >= 5.19 (5.18 doesn't know `embedding_gemma2`).

Usage: python gemma_spike.py <fixtures_dir> <out_dir> [cpu|cuda|mps]
"""
import json
import os
import sys
import time

import numpy as np
import pypdfium2 as pdfium
import torch
from PIL import Image
from sentence_transformers import SentenceTransformer

MODEL = "google/embeddinggemma-2"
REVISION = "914f7f89142e33e77833254d9c9b90c3cef7303b"


def load(device, config_kwargs):
    dtype = torch.bfloat16 if device == "cuda" else torch.float32  # never fp16
    t0 = time.perf_counter()
    m = SentenceTransformer(MODEL, revision=REVISION, device=device,
                            model_kwargs={"dtype": dtype}, config_kwargs=config_kwargs)
    return m, time.perf_counter() - t0


def page_image(path, page, side=1024):
    pdf = pdfium.PdfDocument(path)
    try:
        p = pdf[page]
        w, h = p.get_size()
        return p.render(scale=side / max(w, h)).to_pil().convert("RGB")
    finally:
        pdf.close()


def main():
    fixtures, out = sys.argv[1], sys.argv[2]
    device = sys.argv[3] if len(sys.argv) > 3 else "cpu"
    os.makedirs(out, exist_ok=True)
    omni = json.load(open(os.path.join(os.path.dirname(__file__), "..", "omni", "out", "omni_queries.json")))
    queries = omni["queries"]
    docs = omni.get("doc_chunks") or []

    if device == "cuda":
        torch.cuda.reset_peak_memory_stats()
    m, dt = load(device, {"audio_config": None})
    print(f"load text+vision on {device}: {dt:.1f}s", flush=True)
    if device == "cuda":
        print(f"  {torch.cuda.get_device_name(0)}, peak VRAM after load {torch.cuda.max_memory_allocated() / 1e9:.2f} GB")
    print(f"  prompts: query={m.prompts.get('query')!r} document={m.prompts.get('document')!r}")
    print(f"  dim={m.get_embedding_dimension()} max_seq={m.max_seq_length}")

    # Query vectors (for GGUF parity) and document throughput.
    m.encode_query(queries[:2])  # warmup
    t0 = time.perf_counter()
    q = m.encode_query(queries, batch_size=16, normalize_embeddings=True)
    tq = time.perf_counter() - t0
    print(f"queries: {len(queries)} in {tq:.2f}s ({1000 * tq / len(queries):.1f} ms each, batched)")
    t0 = time.perf_counter()
    for s in queries[:10]:
        m.encode_query([s], normalize_embeddings=True)
    print(f"query one-shot: {1000 * (time.perf_counter() - t0) / 10:.1f} ms each")
    if docs:
        t0 = time.perf_counter()
        d = m.encode_document(docs, batch_size=8, normalize_embeddings=True)
        td = time.perf_counter() - t0
        print(f"doc chunks: {len(docs)} in {td:.2f}s ({1000 * td / len(docs):.1f} ms each)")
    json.dump({"model": f"{MODEL}@{REVISION}", "device": device, "query_prompt": m.prompts.get("query"),
               "queries": queries, "vectors": q.tolist()},
              open(os.path.join(out, f"gemma_queries_{device}.json"), "w"))

    # Image input forms.
    img = Image.open(os.path.join(fixtures, "car.jpg")).convert("RGB")
    img.thumbnail((1024, 1024))
    forms = {
        "pil": img,
        "dict image": {"image": img},
        "dict text+image": {"text": "title: none | text: <|image|>", "image": [img]},
    }
    vecs = {}
    for name, x in forms.items():
        try:
            t0 = time.perf_counter()
            v = m.encode([x], normalize_embeddings=True)[0]
            vecs[name] = v
            print(f"image form {name!r}: ok, {1000 * (time.perf_counter() - t0):.0f} ms")
        except Exception as e:  # report and continue: this is what the spike is for
            print(f"image form {name!r}: FAILED {type(e).__name__}: {str(e)[:200]}")
    try:
        v = m.encode_document([img], normalize_embeddings=True)[0]
        vecs["encode_document(pil)"] = v
        print("image via encode_document(pil): ok")
    except Exception as e:
        print(f"image via encode_document(pil): FAILED {type(e).__name__}: {str(e)[:200]}")
    names = list(vecs)
    for i in range(len(names)):
        for j in range(i + 1, len(names)):
            print(f"  cos({names[i]}, {names[j]}) = {float(vecs[names[i]] @ vecs[names[j]]):.4f}")
    car_q = m.encode_query(["a photo of a car", "transformer attention equations"], normalize_embeddings=True)
    if vecs:
        v = next(iter(vecs.values()))
        print(f"  sanity: cos(car query, image)={float(car_q[0] @ v):.3f} vs unrelated={float(car_q[1] @ v):.3f}")

    # PDF page images: throughput after warmup, fixed size.
    pages = [page_image(os.path.join(fixtures, "attention.pdf"), i) for i in range(6)]
    form = next((f for f in ("pil", "encode_document(pil)", "dict image") if f in vecs), None)
    if form:
        enc = (lambda x: m.encode_document([x], normalize_embeddings=True)) if form == "encode_document(pil)" else \
            (lambda x: m.encode([x if form == "pil" else {"image": x}], normalize_embeddings=True))
        enc(pages[0])
        t0 = time.perf_counter()
        for p in pages[1:]:
            enc(p)
        print(f"page image 1024px ({form}): {(time.perf_counter() - t0) / (len(pages) - 1):.2f} s each")
    if device == "cuda":
        print(f"peak VRAM {torch.cuda.max_memory_allocated() / 1e9:.2f} GB")

    # Text-only loading must give the same text vectors.
    del m
    if device == "cuda":
        torch.cuda.empty_cache()
    mt, dt = load(device, {"audio_config": None, "vision_config": None})
    qt = mt.encode_query(queries, batch_size=16, normalize_embeddings=True)
    cos = (q * qt).sum(1)
    print(f"text-only load {dt:.1f}s; cos(text+vision, text-only) query vectors: min {cos.min():.6f} mean {cos.mean():.6f}")


if __name__ == "__main__":
    main()
