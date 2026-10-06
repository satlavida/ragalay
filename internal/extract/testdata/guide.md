---
title: ragalay guide
source: ../pdf/guide.pdf
---
# ragalay guide

ragalay indexes the documents in a folder so people and AI agents can search them.

![Architecture diagram](img/architecture.png)

## Installing

Download the file for your computer and put it in the folder you want to search.

- Windows: `ragalay.exe`
- macOS: `ragalay`

<!-- page 2 -->

## Searching

### From the terminal

Run `ragalay search "your question"`. Results show the file and the page.

![logo][logo-ref]

<img src="screens/tui%20search.png" alt="The search screen">

```sh
ragalay search "multi-head attention" --json
```

### Images that are skipped

![remote](https://example.com/remote.png)
![missing](img/missing.png)
![outside](../../outside.png)
![vector](img/drawing.svg)

[logo-ref]: logo.png
