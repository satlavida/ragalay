"""ragalay indexing sidecar: embeds documents with the folder's local profile
(jina-embeddings-v5-omni or EmbeddingGemma 2).

Embedded in the ragalay binary and written to the shared cache by
`ragalay setup`. Speaks line-delimited JSON-RPC over stdin/stdout:

  request:  {"id": 1, "method": "embed_documents", "params": {...}}
  response: {"id": 1, "result": {...}}  or  {"id": 1, "error": {"message": "..."}}

The first line written is {"ready": true, ...info} once the model is loaded.
Everything else (library logs, warnings, prints) goes to stderr.

Usage:
  python sidecar.py serve --model M --revision R [--device auto] [--max-side 1024]
  python sidecar.py download --model M --revision R
"""
import argparse
import base64
import json
import os
import sys
import traceback

# Keep stdout for the protocol only: anything else that writes to fd 1
# (prints, C libraries) is sent to stderr.
_proto = os.fdopen(os.dup(1), "w", encoding="utf-8", buffering=1)
os.dup2(2, 1)
sys.stdout = sys.stderr


def send(obj):
    _proto.write(json.dumps(obj, separators=(",", ":")) + "\n")
    _proto.flush()


def download(args):
    from huggingface_hub import constants, snapshot_download

    send({"cache_dir": constants.HF_HUB_CACHE})
    path = snapshot_download(args.model, revision=args.revision)
    send({"done": True, "path": path})


class Embedder:
    def __init__(self, model, revision, device, max_side):
        import torch
        from sentence_transformers import SentenceTransformer

        self.torch = torch
        if device == "auto":
            if torch.cuda.is_available():  # also true for ROCm builds
                device = "cuda"
            elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
                device = "mps"
            else:
                device = "cpu"
        self.gemma = "embeddinggemma" in model.lower()
        if self.gemma:
            # EmbeddingGemma 2 (plan2 §3.1): no remote code; bf16 or fp32,
            # never fp16. The audio encoder is not loaded until audio is
            # indexed (Plan 3); text vectors are identical without it.
            bf16 = device == "cuda" and torch.cuda.is_bf16_supported()
            dtype = torch.bfloat16 if bf16 else torch.float32
            self.model = SentenceTransformer(
                model, device=device, revision=revision, local_files_only=True,
                model_kwargs={"dtype": dtype}, config_kwargs={"audio_config": None})
            self.model.max_seq_length = 8192
        else:
            # Jina's remote code forces bf16 regardless of the dtype argument,
            # so cast after loading. bf16 is ~7x slower than fp32 on CPUs
            # without bf16 support (plan1 §3.1).
            dtype = {"cuda": torch.bfloat16, "mps": torch.float32, "cpu": torch.float32}[device]
            self.model = SentenceTransformer(
                model, trust_remote_code=True, device=device, revision=revision,
                local_files_only=True).to(dtype)
        self.device, self.dtype, self.max_side = device, str(dtype).replace("torch.", ""), max_side
        self.info = {
            "model": model, "revision": revision, "device": device, "dtype": self.dtype,
            "dim": self.model.get_embedding_dimension(),
            "device_name": torch.cuda.get_device_name(0) if device == "cuda" else device,
            "torch": torch.__version__,
        }

    def bucket(self, img):
        """Fit img into one of three fixed shapes (portrait, square, landscape)
        and pad. Fixed shapes avoid per-shape kernel compiles on ROCm."""
        from PIL import Image

        side = max(56, self.max_side // 28 * 28)
        short = max(28, round(side * 0.75 / 28) * 28)
        w, h = img.size
        ratio = w / h
        if ratio < 0.875:
            bw, bh = short, side
        elif ratio > 1.143:
            bw, bh = side, short
        else:
            bw, bh = side, side
        img = img.convert("RGB")
        img.thumbnail((bw, bh))
        canvas = Image.new("RGB", (bw, bh), "white")
        canvas.paste(img, ((bw - img.width) // 2, (bh - img.height) // 2))
        return canvas

    def load_image(self, item):
        from PIL import Image

        if item["modality"] == "pdf_page":
            import pypdfium2 as pdfium

            pdf = pdfium.PdfDocument(item["path"])
            try:
                page = pdf[item.get("page", 0)]
                w, h = page.get_size()
                return self.bucket(page.render(scale=self.max_side / max(w, h)).to_pil())
            finally:
                pdf.close()
        with Image.open(item["path"]) as im:
            im.load()
            return self.bucket(im)

    def encode(self, items, query=False):
        if self.gemma:
            return [b64(v) for v in self.encode_gemma(items, query)]
        enc = self.model.encode_query if query else self.model.encode_document
        out = [None] * len(items)
        texts = [(i, it["text"]) for i, it in enumerate(items) if it["modality"] == "text"]
        if texts:
            vecs = enc([t for _, t in texts], batch_size=8, normalize_embeddings=True)
            for (i, _), v in zip(texts, vecs):
                out[i] = v
        for i, it in enumerate(items):
            if it["modality"] == "text":
                continue
            img = self.load_image(it)
            payload = (it["text"], img) if it.get("text") else img
            out[i] = enc([payload], normalize_embeddings=True)[0]
        return [b64(v) for v in out]

    def encode_gemma(self, items, query):
        """EmbeddingGemma's prompts: "task: search result | query: " for
        queries, "title: {title} | text: " for documents, where the title is
        the file name and heading path (plan2 S9). Images go in as interleaved
        input with an <|image|> placeholder, after the caption if any."""
        out = [None] * len(items)
        texts = [(i, it) for i, it in enumerate(items) if it["modality"] == "text"]
        if query:
            vecs = self.model.encode_query([it["text"] for _, it in texts], batch_size=8, normalize_embeddings=True)
            for (i, _), v in zip(texts, vecs):
                out[i] = v
        else:
            by_title = {}
            for i, it in texts:
                by_title.setdefault(it.get("title") or "none", []).append((i, it["text"]))
            for title, group in by_title.items():
                vecs = self.model.encode([t for _, t in group], prompt=f"title: {title} | text: ",
                                         batch_size=8, normalize_embeddings=True)
                for (i, _), v in zip(group, vecs):
                    out[i] = v
        for i, it in enumerate(items):
            if it["modality"] == "text":
                continue
            img = self.load_image(it)
            caption = (it.get("text") or "").strip()
            prefix = "task: search result | query: " if query else f"title: {it.get('title') or 'none'} | text: "
            text = prefix + (caption + " " if caption else "") + "<|image|>"
            out[i] = self.model.encode([{"text": text, "image": [img]}], normalize_embeddings=True)[0]
        return out


def b64(v):
    import numpy as np

    return base64.b64encode(np.asarray(v, dtype="<f4").tobytes()).decode("ascii")


def serve(args):
    emb = Embedder(args.model, args.revision, args.device, args.max_side)
    send({"ready": True, **emb.info})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        req = {}
        try:
            req = json.loads(line)
            method, params = req.get("method"), req.get("params") or {}
            if method == "info":
                result = emb.info
            elif method == "embed_documents":
                result = {"vectors": emb.encode(params["inputs"])}
            elif method == "embed_query":
                result = {"vectors": emb.encode([params["input"]], query=True)}
            elif method == "shutdown":
                send({"id": req.get("id"), "result": {}})
                return
            else:
                raise ValueError(f"unknown method {method!r}")
            send({"id": req.get("id"), "result": result})
        except Exception as e:  # noqa: BLE001 - report every failure to the caller
            traceback.print_exc()
            send({"id": req.get("id"), "error": {"message": f"{type(e).__name__}: {e}"}})


def cache(args):
    """Size of a model in the Hugging Face cache, or remove it. The cache
    shares blobs between files, so only huggingface_hub can measure and
    delete a model correctly (`ragalay setup --prune`)."""
    from huggingface_hub import scan_cache_dir

    info = scan_cache_dir()
    repo = next((r for r in info.repos if r.repo_id == args.model and r.repo_type == "model"), None)
    if repo is None:
        send({"bytes": 0})
        return
    if args.command == "cache-delete":
        info.delete_revisions(*[r.commit_hash for r in repo.revisions]).execute()
    send({"bytes": repo.size_on_disk})


def main():
    p = argparse.ArgumentParser()
    p.add_argument("command", choices=["serve", "download", "cache-size", "cache-delete"])
    p.add_argument("--model", required=True)
    p.add_argument("--revision", default="")
    p.add_argument("--device", default="auto")
    p.add_argument("--max-side", type=int, default=1024)
    args = p.parse_args()
    try:
        if args.command.startswith("cache-"):
            cache(args)
            return
        (serve if args.command == "serve" else download)(args)
    except Exception as e:  # noqa: BLE001
        traceback.print_exc()
        send({"fatal": f"{type(e).__name__}: {e}"})
        sys.exit(1)


if __name__ == "__main__":
    main()
