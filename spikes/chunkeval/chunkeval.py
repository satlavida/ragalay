"""Phase 0 spike: chunk-size retrieval check (G16) + page-image retrieval.

Each query has an answer phrase; relevant pages = pages whose text contains
the phrase. Retrieval is scored at page level: hit@1, hit@5, MRR.

Usage: python chunkeval.py <fixtures_dir> [device]
"""
import os
import sys
import time

import numpy as np
import pypdfium2 as pdfium
import torch
from sentence_transformers import SentenceTransformer

MODEL = "jinaai/jina-embeddings-v5-omni-small-retrieval"
REVISION = "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4"

QA = [
    ("attention.pdf", "how many identical layers are in the encoder stack", "N = 6"),
    ("attention.pdf", "which optimizer was used for training", "Adam optimizer"),
    ("attention.pdf", "what hardware were the models trained on", "P100"),
    ("attention.pdf", "how is position information injected into the model", "sine and cosine"),
    ("attention.pdf", "what beam size was used during decoding", "beam size of 4"),
    ("attention.pdf", "what regularization smooths the target labels", "Label Smoothing"),
    ("attention.pdf", "results on English constituency parsing", "Penn Treebank"),
    ("attention.pdf", "how many training steps for the big model", "300,000 steps"),
    ("attention.pdf", "what is the dimensionality of the feed-forward inner layer", "dff = 2048"),
    ("attention.pdf", "learning rate warmup schedule", "warmup_steps"),
    ("bert.pdf", "what percentage of tokens are masked during pre-training", "15%"),
    ("bert.pdf", "which corpora were used for pre-training", "BooksCorpus"),
    ("bert.pdf", "how many parameters does the large model have", "340M"),
    ("bert.pdf", "the binarized next sentence prediction task", "Next Sentence Prediction"),
    ("bert.pdf", "results on the SWAG commonsense dataset", "SWAG"),
    ("bert.pdf", "ablation over the number of layers and hidden size", "Effect of Model Size"),
    ("bert.pdf", "using BERT as a feature extractor without fine-tuning", "Feature-based Approach"),
    ("bert.pdf", "fine-tuning batch size and learning rate choices", "Learning rate (Adam)"),
    ("bert.pdf", "comparison with OpenAI GPT", "OpenAI GPT"),
    ("bert.pdf", "named entity recognition CoNLL-2003", "CoNLL-2003"),
]


def load_pages(fixtures):
    pages = []  # (doc, page_idx, text, pil)
    for doc in sorted({d for d, _, _ in QA}):
        pdf = pdfium.PdfDocument(os.path.join(fixtures, doc))
        for i in range(len(pdf)):
            text = pdf[i].get_textpage().get_text_range()
            img = pdf[i].render(scale=2).to_pil().convert("RGB")
            img.thumbnail((1024, 1024))
            pages.append((doc, i, text, img))
    return pages


def chunk_tokens(tok, text, size, overlap):
    ids = tok(text, add_special_tokens=False)["input_ids"]
    if not ids:
        return []
    out, step = [], max(size - overlap, 1)
    for s in range(0, len(ids), step):
        out.append(tok.decode(ids[s:s + size]))
        if s + size >= len(ids):
            break
    return out


def score(q_vecs, unit_vecs, unit_pages, rel):
    hit1 = hit5 = mrr = 0.0
    for qi, q in enumerate(q_vecs):
        order = np.argsort(-(unit_vecs @ q))
        seen, ranked = set(), []
        for u in order:
            p = unit_pages[u]
            if p not in seen:
                seen.add(p)
                ranked.append(p)
        r = next((k for k, p in enumerate(ranked) if p in rel[qi]), None)
        if r is not None:
            hit1 += r == 0
            hit5 += r < 5
            mrr += 1 / (r + 1)
    n = len(q_vecs)
    return hit1 / n, hit5 / n, mrr / n


def main():
    fixtures = sys.argv[1]
    device = sys.argv[2] if len(sys.argv) > 2 else "cpu"
    model = SentenceTransformer(MODEL, trust_remote_code=True, device=device, revision=REVISION)
    model = model.to(torch.bfloat16 if device != "cpu" else torch.float32)
    tok = model.tokenizer

    pages = load_pages(fixtures)
    rel = []
    for doc, _, phrase in QA:
        r = {(d, i) for d, i, t, _ in pages if d == doc and phrase.lower() in t.lower()}
        rel.append(r)
    missing = [QA[i][2] for i, r in enumerate(rel) if not r]
    print(f"{len(pages)} pages, {len(QA)} queries; phrases not found: {missing}")

    q_vecs = model.encode_query([q for _, q, _ in QA], normalize_embeddings=True)

    print(f"{'unit':<22}{'units':>7}{'hit@1':>8}{'hit@5':>8}{'MRR':>8}{'embed s':>9}")
    for name, size, overlap in [("tokens 256/32", 256, 32), ("tokens 512/64", 512, 64),
                                ("tokens 1024/128", 1024, 128), ("whole page", None, None)]:
        units, unit_pages = [], []
        for d, i, t, _ in pages:
            cs = [t] if size is None else chunk_tokens(tok, t, size, overlap)
            units += cs
            unit_pages += [(d, i)] * len(cs)
        t0 = time.perf_counter()
        vecs = model.encode_document(units, batch_size=8, normalize_embeddings=True)
        dt = time.perf_counter() - t0
        h1, h5, m = score(q_vecs, vecs, unit_pages, rel)
        print(f"{name:<22}{len(units):>7}{h1:>8.2f}{h5:>8.2f}{m:>8.3f}{dt:>9.1f}", flush=True)

    t0 = time.perf_counter()
    img_vecs = np.vstack([model.encode_document([p[3]], normalize_embeddings=True) for p in pages])
    dt = time.perf_counter() - t0
    h1, h5, m = score(q_vecs, img_vecs, [(d, i) for d, i, _, _ in pages], rel)
    print(f"{'page image (1024px)':<22}{len(pages):>7}{h1:>8.2f}{h5:>8.2f}{m:>8.3f}{dt:>9.1f}")


if __name__ == "__main__":
    main()
