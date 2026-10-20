# mmdview

Lightweight, live-reloading Mermaid viewer. Serves `.mmd` / `.mermaid` files and ```` ```mermaid ```` blocks in
markdown, re-renders on save, and reports render errors back over HTTP, so an agent can check that a diagram
actually parses.

```
mmdview                    diagrams under the current directory
mmdview docs/              diagrams under docs/
mmdview flow.mmd           a single file
mmdview --help
```

Viewer: file list (newest first, markdown blocks listed by heading), pan/zoom (drag, scroll, pinch, `f` to fit),
source pane with the failing line highlighted, light/dark, SVG/PNG export, and "Follow" mode that jumps to
whichever diagram changed last. Press `?` for shortcuts. Mermaid is bundled in the binary, so it works offline.

API (localhost only):

```
GET  /api/status?path=<file>&wait=<s>   render result for the file's current contents (waits for an open page)
POST /api/open?path=<file>              switch open pages to <file>
```

## Claude Code plugin

The `mermaid` skill (`plugin/skills/mermaid`) has Claude write a diagram file, open it in the viewer, and fix it
until `/api/status` reports it rendered. On first use it downloads the release binary unless `mmdview` is on PATH.

```
/plugin marketplace add atharvakadlag/tools
/plugin install mmdview@atharva-tools
```

## Build

```
make            # build ./mmdview (pure Go; downloads the pinned mermaid.min.js on first build)
make test
make release V=0.2.0   # tag + publish binaries for macOS/Linux × arm64/amd64 (commit and push first)
```
