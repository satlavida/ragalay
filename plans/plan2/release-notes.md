# Release notes: swappable models (Plan 2)

Paste into the draft GitHub release after tagging.

## New: choose your AI model

- **EmbeddingGemma 2 is the new default** for new folders. It's free for any use, including commercial (Apache 2.0), downloads about 1.8 GB, and indexes text about twice as fast as Jina v5 on a CPU.
- **Existing folders keep Jina v5** and their index. Nothing is re-read unless you switch.
- **Use any embedding service** that speaks the OpenAI API: Ollama, LM Studio, llama-server or vLLM on your computer, or an online service. Nothing needs to be installed for these. Images work with services that accept them (Jina API, vLLM, llama-server with a vision projector).
- **Switch from the new Model view** (Tab until it's highlighted) or with `ragalay model use <name>`. Search keeps working on the old model while the new one is built, then switches over by itself.
- **Privacy:** ragalay asks once per folder before sending documents to a service on another computer, and says how much will be sent. API keys are read from an environment variable and never saved in the folder. On Windows the Model view can store the key for you (Ctrl+K).

## Also
- `ragalay setup --prune` lists downloaded models with their sizes, and removes the ones you choose.
- `ragalay model list|show|test`, plus `status --json` and MCP `status`, now report the folder's model.
- `search` no longer stops with exit code 2 after the model settings change: it keeps answering with the index's model and adds a notice. `scan` and `reembed` still use exit code 2.

## After updating
- **Run setup once** (`ragalay setup`, or just open the window). The search engine (llama.cpp) is updated to support the new model: a 19 MB download, plus a small Python library update. PyTorch isn't downloaded again.
- Folders made with the previous version are upgraded automatically the first time they're opened.
