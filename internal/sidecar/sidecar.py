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
            # never fp16. The audio encoder is not loaded until the first
            # audio or video input (media_model); text vectors are identical
            # without it.
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
        self.model_args = (model, revision, dtype)
        self.has_media = False
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

    def media_model(self):
        """Gemma 2 with its audio encoder (plan3 D3). Loaded on the first
        audio or video input and kept for the rest of the run; the text
        vectors do not change."""
        if not self.gemma:
            raise ValueError("this model cannot read audio or video; use EmbeddingGemma 2")
        if not self.has_media:
            import gc
            from sentence_transformers import SentenceTransformer

            model, revision, dtype = self.model_args
            self.model = None
            gc.collect()
            self.model = SentenceTransformer(
                model, device=self.device, revision=revision, local_files_only=True,
                model_kwargs={"dtype": dtype})
            self.model.max_seq_length = 8192
            self.has_media = True
        return self.model

    def encode_media(self, items):
        """One vector per audio or video window. Python decodes with PyAV:
        audio becomes 16 kHz mono float32, video becomes frames at 1 per
        second (plan3 D2/D3). Media goes in without a prompt prefix."""
        model = self.media_model()
        out = []
        for it in items:
            if it["modality"] == "audio":
                x = {"audio": decode_audio(it["path"], it.get("start_ms", 0), it.get("end_ms", 0))}
            else:
                x = {"video": decode_frames(it["path"], it.get("start_ms", 0), it.get("end_ms", 0), self.max_side)}
            out.append(model.encode(x, normalize_embeddings=True))
        return out

    def encode(self, items, query=False):
        media = [(i, it) for i, it in enumerate(items) if it["modality"] in ("audio", "video")]
        if media:
            vecs = self.encode_media([it for _, it in media])
            rest = [it for it in items if it["modality"] not in ("audio", "video")]
            rest_vecs = self.encode(rest, query) if rest else []
            out, ri, mi = [], iter(rest_vecs), iter(b64(v) for v in vecs)
            for it in items:
                out.append(next(mi) if it["modality"] in ("audio", "video") else next(ri))
            return out
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


SAMPLE_RATE = 16000


def decode_audio(path, start_ms, end_ms):
    """Mono 16 kHz float32 samples of [start_ms, end_ms) of the first audio
    stream (end_ms 0 = to the end), capped at the model's 30 s."""
    import av
    import numpy as np

    limit = int(SAMPLE_RATE * 30)
    start, want = start_ms / 1000.0, (end_ms - start_ms) / 1000.0 if end_ms > start_ms else 30.0
    want = min(want, 30.0)
    with av.open(path) as c:
        if not c.streams.audio:
            raise ValueError("the file has no audio track")
        stream = c.streams.audio[0]
        if start > 0:
            c.seek(int(start * av.time_base))
        rs = av.AudioResampler(format="flt", layout="mono", rate=SAMPLE_RATE)
        chunks, n, skip = [], 0, None
        for frame in c.decode(stream):
            if skip is None:
                # Seeking lands on a keyframe before start; drop the lead-in.
                skip = max(0, int((start - float(frame.time or 0)) * SAMPLE_RATE)) if start > 0 else 0
            for f in rs.resample(frame) or []:
                chunks.append(f.to_ndarray().reshape(-1))
                n += len(chunks[-1])
            if n - skip >= want * SAMPLE_RATE:
                break
        for f in rs.resample(None) or []:
            chunks.append(f.to_ndarray().reshape(-1))
    if not chunks:
        raise ValueError("no audio could be decoded")
    x = np.concatenate(chunks)[skip or 0:]
    return np.ascontiguousarray(x[: min(int(want * SAMPLE_RATE), limit)], dtype="float32")


def decode_frames(path, start_ms, end_ms, max_side):
    """Frames at 1 per second from [start_ms, end_ms), at most 32, each
    scaled to fit max_side, as one (frames, height, width, 3) uint8 array."""
    import av
    import numpy as np

    start = start_ms / 1000.0
    end = end_ms / 1000.0 if end_ms > start_ms else start + 32.0
    with av.open(path) as c:
        if not c.streams.video:
            raise ValueError("the file has no video track")
        stream = c.streams.video[0]
        if start > 0:
            c.seek(int(start * av.time_base), stream=stream)
        frames, nxt = [], start
        for frame in c.decode(stream):
            t = float(frame.time or 0)
            if t < nxt:
                continue
            if t >= end or len(frames) >= 32:
                break
            scale = min(1.0, max_side / max(frame.width, frame.height))
            w, h = max(2, int(frame.width * scale) // 2 * 2), max(2, int(frame.height * scale) // 2 * 2)
            frames.append(frame.reformat(width=w, height=h, format="rgb24").to_ndarray())
            nxt += 1.0
    if not frames:
        raise ValueError("no video frames could be decoded")
    return np.stack(frames)


def probe(path):
    """Duration and streams of a media file, for window planning."""
    import av

    with av.open(path) as c:
        dur = c.duration / av.time_base if c.duration else 0.0
        if not dur:
            for s in list(c.streams.video) + list(c.streams.audio):
                if s.duration and s.time_base:
                    dur = max(dur, float(s.duration * s.time_base))
        return {"duration_ms": int(dur * 1000), "has_audio": bool(c.streams.audio), "has_video": bool(c.streams.video)}


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
            elif method == "probe":
                result = probe(params["path"])
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
