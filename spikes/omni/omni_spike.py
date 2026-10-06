"""Phase 0 spike: jina-embeddings-v5-omni-small-retrieval on this machine.

Measures load time, per-modality throughput and peak RSS, checks combined
image+text input, and dumps query vectors for the GGUF parity check.

Usage: python omni_spike.py <fixtures_dir> <out_dir> [device]
"""
import json
import os

import torch
import sys
import time

import numpy as np
import psutil
import pypdfium2 as pdfium
from PIL import Image
from sentence_transformers import SentenceTransformer

MODEL = "jinaai/jina-embeddings-v5-omni-small-retrieval"
REVISION = "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4"  # pin remote code

QUERIES = [
    "what is the transformer architecture",
    "how does multi-head attention work",
    "scaled dot-product attention formula",
    "positional encoding sine cosine",
    "BLEU score on English to German translation",
    "training time on eight GPUs",
    "label smoothing regularization",
    "encoder decoder stacks with six layers",
    "why self-attention instead of recurrence",
    "computational complexity per layer",
    "a red car parked on the street",
    "vehicle photo",
    "Which planet is known as the Red Planet?",
    "recipe for chocolate chip cookies",
    "how to reset a forgotten password",
    "quarterly revenue growth report",
    "symptoms of the common cold",
    "python list comprehension example",
    "best hiking trails near mountains",
    "history of the roman empire",
    "how do vaccines train the immune system",
    "difference between TCP and UDP",
    "what is a vector database",
    "embedding models for semantic search",
    "reciprocal rank fusion hybrid search",
    "BM25 keyword ranking",
    "how to chunk documents for retrieval",
    "matryoshka representation learning",
    "GPU vs CPU inference speed",
    "Adam optimizer learning rate schedule",
    "dropout rate used in the base model",
    "beam search with length penalty",
    "byte pair encoding vocabulary size",
    "WMT 2014 dataset",
    "attention visualizations of long-distance dependencies",
    "layer normalization and residual connections",
    "feed-forward network inner dimension",
    "machine translation state of the art",
    "constituency parsing results",
    "who wrote attention is all you need",
    "Go programming language goroutines",
    "how to make a terminal user interface",
    "SQLite full text search",
    "photo of a sports car",
    "a diagram of a neural network",
    "table of results with model variations",
    "cost of training in FLOPs",
    "how to bake sourdough bread",
    "climate change effects on oceans",
    "what time zone is India in",
]


def rss_gb():
    return psutil.Process().memory_info().rss / 1e9


def timed(label, fn, n_items, results):
    t = time.perf_counter()
    out = fn()
    dt = time.perf_counter() - t
    results[label] = {"seconds": round(dt, 3), "items": n_items,
                      "per_item_s": round(dt / max(n_items, 1), 3),
                      "rss_gb": round(rss_gb(), 2)}
    print(f"{label}: {dt:.2f}s for {n_items} ({dt / max(n_items, 1):.3f}s/item), rss {rss_gb():.2f} GB", flush=True)
    return out


def cos(a, b):
    a, b = np.asarray(a, dtype=np.float32), np.asarray(b, dtype=np.float32)
    return float(a @ b / (np.linalg.norm(a) * np.linalg.norm(b)))


def main():
    fixtures, out_dir = sys.argv[1], sys.argv[2]
    device = sys.argv[3] if len(sys.argv) > 3 else "cpu"
    os.makedirs(out_dir, exist_ok=True)
    res = {"device": device, "rss_start_gb": round(rss_gb(), 2)}

    dtype = os.environ.get("OMNI_DTYPE", "float32")
    res["dtype"] = dtype
    model = timed("load", lambda: SentenceTransformer(
        MODEL, trust_remote_code=True, device=device, revision=REVISION,
        model_kwargs={"torch_dtype": getattr(torch, dtype)}), 1, res)
    res["prompts"] = getattr(model, "prompts", None)
    res["dim"] = model.get_embedding_dimension()
    print("prompts:", res["prompts"], "dim:", res["dim"], flush=True)

    pdf = pdfium.PdfDocument(os.path.join(fixtures, "attention.pdf"))
    page_texts = [pdf[i].get_textpage().get_text_range() for i in range(len(pdf))]
    chunks = []
    for t in page_texts:
        words = t.split()
        for i in range(0, len(words), 300):  # ~400 tokens per chunk
            chunks.append(" ".join(words[i:i + 300]))
    chunks = chunks[:32]
    res["pdf_pages"] = len(pdf)

    doc_vecs = timed("text_documents", lambda: model.encode_document(chunks, batch_size=8), len(chunks), res)
    q_vecs = timed("text_queries", lambda: model.encode_query(QUERIES, batch_size=16), len(QUERIES), res)

    with open(os.path.join(out_dir, "omni_queries.json"), "w", encoding="utf-8") as f:
        json.dump({"model": MODEL, "queries": QUERIES, "vectors": np.asarray(q_vecs).tolist(),
                   "doc_chunks": chunks, "doc_vectors": np.asarray(doc_vecs).tolist()}, f)

    car = Image.open(os.path.join(fixtures, "car.jpg")).convert("RGB")
    img_vec = timed("image", lambda: model.encode_document([car]), 1, res)

    pages = [pdf[i].render(scale=150 / 72).to_pil().convert("RGB") for i in range(3)]
    page_vecs = timed("pdf_page_images", lambda: model.encode_document(pages, batch_size=1), len(pages), res)

    # Image-only ("scanned") PDF: no text layer, must still render and embed.
    scanned = os.path.join(out_dir, "scanned.pdf")
    pages[0].save(scanned)
    sp = pdfium.PdfDocument(scanned)
    res["scanned_text_chars"] = len(sp[0].get_textpage().get_text_range().strip())
    scanned_vec = model.encode_document([sp[0].render(scale=150 / 72).to_pil().convert("RGB")])

    try:
        fused = timed("fused_text_image", lambda: model.encode_document([("A red sports car on a street", car)]), 1, res)
        res["fused_supported"] = True
        res["fused_vs_image_cos"] = round(cos(fused[0], img_vec[0]), 4)
    except Exception as e:  # noqa: BLE001
        res["fused_supported"] = False
        res["fused_error"] = repr(e)
        print("fused failed:", e, flush=True)

    # Sanity retrieval checks.
    qi = {q: i for i, q in enumerate(QUERIES)}
    res["sanity"] = {
        "car_query_vs_car_image": round(cos(q_vecs[qi["photo of a sports car"]], img_vec[0]), 4),
        "car_query_vs_page1": round(cos(q_vecs[qi["photo of a sports car"]], page_vecs[0]), 4),
        "attention_query_vs_page1": round(cos(q_vecs[qi["what is the transformer architecture"]], page_vecs[0]), 4),
        "attention_query_vs_car_image": round(cos(q_vecs[qi["what is the transformer architecture"]], img_vec[0]), 4),
        "scanned_vs_page1": round(cos(scanned_vec[0], page_vecs[0]), 4),
        "top_chunk_for_mha": int(np.argmax(doc_vecs @ q_vecs[qi["how does multi-head attention work"]])),
    }
    res["rss_peak_gb"] = round(rss_gb(), 2)

    with open(os.path.join(out_dir, f"omni_results_{device}.json"), "w", encoding="utf-8") as f:
        json.dump(res, f, indent=2, default=str)
    print(json.dumps(res, indent=2, default=str))


if __name__ == "__main__":
    main()
