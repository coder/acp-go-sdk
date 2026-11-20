# Releasing

The SDK has its own semver in the top-level `version` file. The ACP schema
release that the generated code follows is pinned separately in
`schema/version`, and the SDK exports it as `acp.SchemaVersion`. A release can
change the SDK without a new schema, and a schema bump does not set the SDK
version.

The SDK stays on v0. Raise the minor version for a schema bump or an API break,
and the patch version for fixes only.

A release goes out in two steps. A release PR lands through review and the
merge queue. Then the [Release workflow](.github/workflows/release.yaml) tags
its commit and publishes it.

## Prerequisites

- `make`, `curl`, `git`, and `gh` in your `PATH`, with `gh` logged in as
  yourself.
- [mise](https://mise.jdx.dev) for the toolchain. Run `mise install` once to
  provision Go, `treefmt`, and the formatters and linters that `make fmt` and
  `make check` invoke.
- The repository settings in
  [One-time repository setup](#one-time-repository-setup).

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
   and the merge queue. Then [publish](#publish) it.

## Publish

The [Release workflow](.github/workflows/release.yaml) tags the release commit,
creates the GitHub release, and waits until the Go module proxy serves the new
version. Run it from `main` after the release PR merges. The examples use
`v0.21.0` and ACP schema `1.32.0`.

1. Start a dry run. In the **Actions** tab, open **Release**, and choose
   **Run workflow**. Enter the version, and check **dry_run**. Or run:

   ```bash
   gh workflow run release.yaml --ref main -f version=0.21.0 -f schema=1.32.0 -f dry_run=true
   ```

   A dry run runs only the `preflight` job and writes nothing. The `schema`
   input is optional. When you set it, it must equal `schema/version` at the
   release commit.

1. Read the release plan in the summary of the run. It lists the tag, the
   release commit, the schema, the module, and the environment.

1. Decide whether to go on. A pushed tag is permanent. The Go module proxy
   keeps every version that it serves, so you cannot change a tag after
   `publish` pushes it. You can only retract it in a later release. The dry run
   and the approval are the last chance to stop.

1. Start the real run with the same inputs and without `dry_run`:

   ```bash
   gh workflow run release.yaml --ref main -f version=0.21.0 -f schema=1.32.0
   ```

1. When the `release` environment exists, GitHub holds the `publish` job until
   ThomasK33 approves it. Open the run, and choose **Review deployments**. Check `release`,
   and choose **Approve and deploy**. Without the environment, `publish` starts
   at once, and `preflight` warns about it.

1. Wait for the `proxy` and `notes` jobs. Announce the release only after
   `proxy` passes.

### What each job does

The `preflight` job computes the release plan once. The later jobs read the
plan from its outputs and compute nothing again.

- `preflight` checks the release and changes nothing. It runs every check,
  lists every one that fails, and fails unless all of these hold:
  - The version is a semver. A real run starts from `main`.
  - The `version` file on `main` holds the version. The release commit is the
    last commit on `main` that changed `version`.
  - `schema/version` at the release commit equals the `schema` input, when you
    set it. `constants_gen.go` sets `SchemaVersion` to the same schema.
  - The module path in `go.mod` matches the repository, so the Go module proxy
    can serve the tag.
  - No tag and no GitHub release exist for the version.
  - The CI run for the push of the release commit passed.
- `publish` waits for approval in the `release` environment, when the
  environment exists. Then it signs as `coder-release-publisher` and creates
  the annotated tag `v0.21.0` with the message `v0.21.0 (ACP schema 1.32.0)`.
  With `GITHUB_TOKEN`, it creates the GitHub release `v0.21.0` with the notes
  `ACP schema 1.32.0`. A version with a `-`, such as
  `0.21.0-rc.1`, becomes a prerelease.
- `proxy` asks `proxy.golang.org` for the new version for about 12 minutes. It
  passes when the proxy serves the version at the release commit.
- `notes` calls the
  [release-notes workflow](.github/workflows/release-notes.yaml). It runs
  `communique` and replaces the release body with generated notes. GitHub starts
  no workflow for a release that the Actions `GITHUB_TOKEN` creates, so the
  Release workflow calls the release-notes workflow directly.

### Resume a failed run

When a job after `preflight` fails, fix the cause. Then open the failed run and
choose **Re-run failed jobs**. Do not start a new run, because its `preflight`
refuses a tag that exists.

- `publish` reads the current state first. It keeps a tag that points at the
  release commit and a release that exists, and creates only what is missing.
  It fails on a tag that points at another commit. A re-run of `publish` asks
  for approval again.
- A re-run of `proxy` or `notes` repeats its work.

Nobody can push a release tag by hand once the `Release tags (App only)`
ruleset exists, so the Release workflow is the only way to tag a release.

To regenerate the notes of an existing release, run:

```bash
gh workflow run release-notes.yaml --ref main -f tag=v0.21.0
```

## One-time repository setup

The `release` environment and the `Release tags (App only)` ruleset are
managed as code in
[coder/github-org-terraform](https://github.com/coder/github-org-terraform), in
`orgs/coder/repos/acp-go-sdk.yaml`. Change them there with a PR, never in the
repository settings: the next apply reverts a change made by hand.

- The `release` environment holds the `publish` job until ThomasK33 approves
  it. Only `main` may deploy to it. **Prevent self-review** is off, because the
  one maintainer both starts and approves a release.
- The `Release tags (App only)` ruleset targets `refs/tags/v*`. It restricts
  creations, updates, and deletions to its one bypass actor, the
  `coder-release-publisher` GitHub App. Nobody else can create, move, or delete
  a release tag, repository admins included.

The `publish` job signs as `coder-release-publisher` through
[coder/github-app-token-action](https://github.com/coder/github-app-token-action):
Cloud KMS holds the App's key, and only this job may use it. Until all of the
steps below are done, `publish` creates the tag with `GITHUB_TOKEN`, which works
only while the ruleset does not exist.

1. An organization owner installs `coder-release-publisher` on
   `coder/acp-go-sdk`.

1. In [coder/gcp](https://github.com/coder/gcp)
   `projects/production/coder-github-apps`, add a consumer of
   `coder-release-publisher` to `apps.tf`, with repository `coder/acp-go-sdk`,
   environment `release`, and workflow
   `/.github/workflows/release.yaml@refs/heads/main`, and add the repository to
   `local.github_repository_ids` in `identity.tf`.

1. A repository admin stores the App's settings as secrets of the `release`
   environment, with the values that other `coder-release-publisher` consumers
   use:

   ```bash
   gh secret set CODER_RELEASE_PUBLISHER_APP_ID --repo coder/acp-go-sdk --env release
   gh secret set CODER_RELEASE_PUBLISHER_APP_KMS_KEY --repo coder/acp-go-sdk --env release
   gh secret set APP_KEYS_WORKLOAD_ID_PROVIDER --repo coder/acp-go-sdk --env release
   ```

1. Merge and apply the github-org-terraform PR that adds the environment and
   the ruleset. Apply it after the first three steps, or the ruleset blocks
   every new release tag until they are done.

The release-notes workflow reads the repository secret `ANTHROPIC_API_KEY`,
which exists already. To replace it, run
`gh secret set ANTHROPIC_API_KEY --repo coder/acp-go-sdk`, which prompts for the
value.

Without the `release` environment, the Release workflow still runs. `preflight`
then warns that nothing holds `publish` for a review, and `publish` names no
environment: GitHub would create a missing environment, without protection, the
first time a job names it.

## Additional notes

- Release-note automation updates GitHub Release bodies only. It does not
  maintain a `CHANGELOG.md`.
- The Makefile uses a repository-local Go build cache (`.gocache`). You can
  delete it with `rm -rf .gocache`.
- `make clean` removes the downloaded schema files. Run `make generate` to
  download them again.
