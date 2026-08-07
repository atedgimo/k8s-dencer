# Diagrams

Shareable renders of how the product works, for talks, posts and issues — the
kind of thing you paste somewhere that cannot render Mermaid.

The README's own architecture diagram stays in Mermaid, because GitHub renders
it natively and it stays readable to a screen reader. These are the same story
drawn for places that only accept an image.

| File | What it shows |
|---|---|
| `architecture.svg` / `.png` | The path from watching the cluster to evicting a pod, and the human approval the whole thing is built around |

The SVG is the source. Regenerate the PNG with:

```bash
make diagrams
```

Both use the product's own palette and IBM Plex, inlined from the UI's font
dependencies rather than the system's — see `ui/brand/render-svg.mjs` for why
that matters.
