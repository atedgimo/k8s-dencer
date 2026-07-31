# Contributing

Thanks for looking at k8s-dencer. Contributions are welcome — issues, docs,
code — and this page is what you need to know before opening one.

## The one rule that shapes everything

**This product predicts honestly and lets a human approve.** Every change is
weighed against that: a feature that makes the tool more autonomous, or a
number that presents a guess as a measurement, will be declined however well
it is built. Read [docs/findings.md](docs/findings.md) to see how seriously
this is taken — most of the bugs recorded there are some form of "reported
success while doing nothing".

## Getting a dev environment

```bash
make demo     # KWOK fake-node fabric + build + install — no real cluster needed
make token    # mint an operator token
make ui       # port-forward and print the URL
```

`make` with no target lists everything. The full loop is in
[docs/development.md](docs/development.md).

## Before you open a PR

- `go test ./...` and `make lint` — lint includes the chart contract checks
  and the privilege-split assertions
- UI changes: `npm run typecheck`, `npm run test:density`, and
  `go test ./test/palette/ ./test/ui/` — the palette test enforces the
  product's visual identity (risk hexes byte-identical, glyph + word, chroma
  budget) and will fail styling that violates it
- **If you write a guard or assertion, break the thing it guards and confirm
  it screams.** Assertions that cannot fail have shipped here before; several
  tests exist specifically to catch that class, and reviewers will ask.

## PRs

- `main` is protected; everything goes through a PR with green CI
- One concern per PR; commit messages explain *why*, in prose
- New payload fields need a reader — a contract test fails both on fields the
  UI never reads and on fields the payload stopped sending

## Reporting security issues

Not in the issue tracker — see [SECURITY.md](SECURITY.md).
