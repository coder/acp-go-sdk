# Releasing

The SDK has its own semver in the top-level `version` file. The ACP schema
release that the generated code follows is pinned separately in
`schema/version`, and the SDK exports it as `acp.SchemaVersion`. A release can
change the SDK without a new schema, and a schema bump does not set the SDK
version.

The SDK stays on v0. Raise the minor version for a schema bump or an API break,
and the patch version for fixes only.

Releases are cut by hand: a release PR lands through review and the merge
queue, and then you tag and publish its merge commit.

## Prerequisites

- `make`, `curl`, `git`, and `gh` in your `PATH`, with `gh` logged in as
  yourself.
- [mise](https://mise.jdx.dev) for the toolchain. Run `mise install` once to
  provision Go, `treefmt`, and the formatters and linters that `make fmt` and
  `make check` invoke.
- An Actions secret named `ANTHROPIC_API_KEY`, which the release-notes workflow
  needs.

## Prepare the release PR

1. Pick the SDK version, for example `0.14.0`. When the release also moves the
   schema, pick the upstream schema version too, for example `1.18.0`.

1. Run the release helper:

   ```bash
   make release VERSION=0.14.0 SCHEMA=1.18.0
   ```

   Leave out `SCHEMA` when the schema stays the same. The helper writes
   `version`, writes `schema/version` when `SCHEMA` is set, runs
   `make generate`, `make fmt`, and `make test`, and then prints the next step.
   `make generate` downloads the schema files for the new `schema/version` and
   regenerates the `*_gen.go` files. It never writes `version`.

1. If the new schema breaks hand-written code, examples, or docs, fix them in
   the same PR, and rerun `make test`.

1. Name every break in the release notes. The `verify-schema-bump` skill lists
   them (G1). Name each optional interface that the schema renames or removes,
   with its replacement. A method that becomes stable moves from, for example,
   `AgentUnstableForkSession` (method `UnstableForkSession`) to
   `AgentForkSession` (method `ForkSession`). An implementer that does not
   assert the old interface keeps compiling, and its peer then gets `-32601`
   for the method. `acp.Agent` and `acp.Client` never change: their methods are
   frozen in `cmd/generate/internal/ir/baseline.go`.

1. Commit the change. Then run `make check`, which fails on any formatting
   drift.

1. Open a PR. Its CI runs `make check` and `make test`. Merge it through review
   and the merge queue.

## Tag and publish

Run these steps on the merge commit of the release PR. The examples use
`v0.14.0` and ACP schema `1.18.0`.

1. Find the merge commit, and check that it carries the expected versions:

   ```bash
   git fetch origin
   commit=$(gh pr view <release-pr> --json mergeCommit --jq .mergeCommit.oid)
   git show "$commit:version" "$commit:schema/version"
   ```

1. Create an annotated tag on that commit, and push it:

   ```bash
   git tag -a v0.14.0 -m "v0.14.0 (ACP schema 1.18.0)" "$commit"
   git push origin v0.14.0
   ```

1. Create the GitHub release from the pushed tag:

   ```bash
   gh release create v0.14.0 --verify-tag --title v0.14.0 --notes "ACP schema 1.18.0"
   ```

   The `release: published` event starts the
   [release-notes workflow](.github/workflows/release-notes.yaml). It runs
   `communique` and replaces the release body with generated notes. GitHub
   starts no workflow for an event that an Actions `GITHUB_TOKEN` causes, so
   create the release with a user token, such as your own `gh` login.

1. Check that the Go module proxy serves the new version:

   ```bash
   GOPROXY=https://proxy.golang.org go list -m github.com/coder/acp-go-sdk@v0.14.0
   ```

   Consumers install the SDK by its `vX.Y.Z` tag through `go get`. Announce the
   release only after this command prints the version.

## Additional notes

- Release-note automation updates GitHub Release bodies only. It does not
  maintain a `CHANGELOG.md`.
- The Makefile uses a repository-local Go build cache (`.gocache`). You can
  delete it with `rm -rf .gocache`.
- `make clean` removes the downloaded schema files. Run `make generate` to
  download them again.
