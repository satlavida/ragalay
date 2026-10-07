"""Plan 2 Phase 0 spike: retrieval of Jina v5 omni vs EmbeddingGemma 2 (S7–S9).

Same method as spikes/chunkeval: each query has an answer phrase, relevant
pages are the pages containing it, scoring is page level (hit@1, hit@5,
hit@10 = recall@10 with one relevant set per query, MRR). Text is chunked at
256/32 tokens with each model's own tokenizer, as ragalay does.

Usage: python eval.py <fixtures_dir> [cpu|cuda]   (needs transformers >= 5.19)
"""
import os
import sys
import time

import numpy as np
import torch
from sentence_transformers import SentenceTransformer

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "chunkeval"))
import chunkeval as ce  # noqa: E402  (QA set, page loading, chunking, scoring)

JINA = ("jinaai/jina-embeddings-v5-omni-small-retrieval", "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4")
GEMMA = ("google/embeddinggemma-2", "914f7f89142e33e77833254d9c9b90c3cef7303b")

# 10 more queries on the same papers, so one query is ~3 points, not 5.
EXTRA = [
    ("attention.pdf", "which attention function scales the dot products", "Scaled Dot-Product Attention"),
    ("attention.pdf", "attending to information from different representation subspaces", "Multi-Head Attention"),
    ("attention.pdf", "what dropout rate was applied to the base model", "Pdrop = 0.1"),
    ("attention.pdf", "english to german translation benchmark dataset", "WMT 2014 English-German"),
    ("attention.pdf", "why self-attention: complexity per layer comparison", "Complexity per Layer"),
    ("bert.pdf", "general language understanding evaluation benchmark", "GLUE"),
    ("bert.pdf", "question answering dataset with 100k crowdsourced pairs", "SQuAD v1.1"),
    ("bert.pdf", "special classification token at the start of every sequence", "[CLS]"),
    ("bert.pdf", "subword vocabulary used for tokenization", "WordPiece"),
    ("bert.pdf", "masked language model pre-training objective", "masked LM"),
]


def recall10(q_vecs, unit_vecs, unit_pages, rel):
    hits = 0
    for qi, q in enumerate(q_vecs):
        order = np.argsort(-(unit_vecs @ q))
        seen, ranked = set(), []
        for u in order:
            if unit_pages[u] not in seen:
                seen.add(unit_pages[u])
                ranked.append(unit_pages[u])
            if len(ranked) == 10:
                break
        hits += any(p in rel[qi] for p in ranked)
    return hits / len(q_vecs)


def fit(v, dim):
    v = v[:, :dim]
    return v / np.linalg.norm(v, axis=1, keepdims=True)


def main():
    fixtures = sys.argv[1]
    device = sys.argv[2] if len(sys.argv) > 2 else "cpu"
    qa = ce.QA + EXTRA
    ce.QA = qa  # load_pages reads the doc list from ce.QA
    pages = ce.load_pages(fixtures)
    rel = [{(d, i) for d, i, t, _ in pages if d == doc and phrase.lower() in t.lower()} for doc, _, phrase in qa]
    missing = [qa[i][2] for i, r in enumerate(rel) if not r]
    print(f"{len(pages)} pages, {len(qa)} queries; phrases not found: {missing}")
    keep = [i for i, r in enumerate(rel) if r]
    qa, rel = [qa[i] for i in keep], [rel[i] for i in keep]
    queries = [q for _, q, _ in qa]

    rows = []

    def report(name, qv, uv, up, secs):
        h1, h5, m = ce.score(qv, uv, up, rel)
        r10 = recall10(qv, uv, up, rel)
        rows.append((name, h1, h5, r10, m, secs))
        print(f"{name:<34}{h1:>7.2f}{h5:>7.2f}{r10:>8.2f}{m:>8.3f}{secs:>8.1f}", flush=True)

    print(f"{'variant':<34}{'hit@1':>7}{'hit@5':>7}{'rec@10':>8}{'MRR':>8}{'secs':>8}")
    # Gemma first: Jina's remote code patches shared state and breaks Gemma image
    # inputs in the same process (one model per sidecar process in ragalay).
    for name, (model_id, rev) in (("gemma", GEMMA), ("jina", JINA)):
        kw = dict(revision=rev, device=device)
        if name == "jina":
            m = SentenceTransformer(model_id, trust_remote_code=True, **kw)
            m = m.to(torch.bfloat16 if device != "cpu" else torch.float32)
        else:
            dtype = torch.bfloat16 if device == "cuda" else torch.float32
            m = SentenceTransformer(model_id, model_kwargs={"dtype": dtype},
                                    config_kwargs={"audio_config": None}, **kw)
            m.max_seq_length = 8192
        units, unit_pages, titles = [], [], []
        for d, i, t, _ in pages:
            cs = ce.chunk_tokens(m.tokenizer, t, 256, 32)
            units += cs
            unit_pages += [(d, i)] * len(cs)
            titles += [d] * len(cs)
        qv = m.encode_query(queries, normalize_embeddings=True)
        t0 = time.perf_counter()
        dv = m.encode_document(units, batch_size=8, normalize_embeddings=True)
        dt = time.perf_counter() - t0
        dim = dv.shape[1]
        report(f"{name} text {dim} (title none)" if name == "gemma" else f"{name} text {dim}", qv, dv, unit_pages, dt)
        for small in (256, 128) if name == "gemma" else (256,):
            report(f"{name} text {small} (truncated)", fit(qv, small), fit(dv, small), unit_pages, 0)
        if name == "gemma":
            t0 = time.perf_counter()
            dvt = np.vstack([m.encode([f"title: {ti} | text: {u}"], normalize_embeddings=True)
                             for ti, u in zip(titles, units)])
            report(f"gemma text {dim} (title=file name)", qv, dvt, unit_pages, time.perf_counter() - t0)
            report("gemma text 256 (title=file name)", fit(qv, 256), fit(dvt, 256), unit_pages, 0)
        t0 = time.perf_counter()
        iv = np.vstack([m.encode_document([p[3]], normalize_embeddings=True) for p in pages])
        report(f"{name} page image 1024px", qv, iv, [(d, i) for d, i, _, _ in pages], time.perf_counter() - t0)
        del m
        if device == "cuda":
            torch.cuda.empty_cache()


if __name__ == "__main__":
    main()
