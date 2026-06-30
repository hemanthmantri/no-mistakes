---
title: Provider Integration
description: Set up GitHub, GitLab, Bitbucket Cloud, or Harness Code for PR creation and CI monitoring.
---

The PR and CI steps need to talk to your git host. Four hosts are supported:
GitHub, GitLab, Bitbucket Cloud (`bitbucket.org`), and Harness Code
(`*.harness.io` SaaS plus self-hosted via `HARNESS_HOST`). Everything else
short-circuits the PR and CI steps with `skipped`.

Provider integration is optional for the local gate. You only need it for the
steps that happen after validation: opening or updating the PR, watching hosted
CI, and fixing remote-only failures.

Without any provider setup, `no-mistakes` still gives you the local gate:

- rebase
- review
- test
- document
- lint
- push through normal Git transport

What you do not get is PR automation and CI monitoring.

## What each step needs

| Step | GitHub | GitLab | Bitbucket Cloud | Harness Code |
|---|---|---|---|---|
| **PR** (create/update) | `gh` CLI, authenticated | `glab` CLI, authenticated | `NO_MISTAKES_BITBUCKET_EMAIL` + `NO_MISTAKES_BITBUCKET_API_TOKEN` | `harness` CLI logged in to the repo's account, OR `HARNESS_API_KEY` |
| **CI** (polling, auto-fix) | `gh` CLI | `glab` CLI | same env vars | not supported yet |
| **Merge conflict auto-fix** | `gh` CLI | `glab` CLI | not supported | not supported |
| **Mergeability polling** | `gh` CLI | `glab` CLI | not supported | not supported |

## What changes when provider wiring is present

Once the host is wired up, `no-mistakes` can keep owning the branch after it
pushes to the configured target:

- create or update the PR automatically
- keep polling hosted CI until the PR is merged, closed, declined, or the configured `ci_timeout` idle window elapses
- fetch failing job logs for the CI auto-fix loop
- on GitHub and GitLab, watch mergeability and fix merge conflicts when possible

## GitHub

Install the GitHub CLI and authenticate:

```sh
# macOS
brew install gh

# Linux
# see https://github.com/cli/cli/blob/trunk/docs/install_linux.md

gh auth login
```

Verify:

```sh
gh auth status
```

`no-mistakes doctor` also checks for `gh` availability.
For PR and workflow-run commands, no-mistakes passes the repository slug from the recorded upstream remote or PR URL to `gh`, so daemon-run commands do not depend on the daemon's current working directory.

**What you get:**

- PR creation and update on pushes
- CI check polling with exponential backoff (30s → 60s → 120s) until the PR is merged, closed, or the configured `ci_timeout` idle window elapses
- Failed job log fetching (`gh run view --log-failed`) for the CI auto-fix step
- PR mergeability polling, and agent-driven resolution when the provider reports an actual merge conflict

### GitHub fork contributions

Fork routing is available for GitHub when you need to push branches to your fork but open PRs against the parent repository.
Keep `origin` pointed at the parent repository, then initialize with your fork URL:

```sh
git remote set-url origin git@github.com:parent-owner/repo.git
no-mistakes init --fork-url git@github.com:your-user/repo.git
```

With this setup, the push and CI auto-fix push steps update the fork, while the PR and CI steps stay scoped to the parent repository.
The GitHub PR step opens PRs with a fork-qualified head such as `your-user:feature-branch`.
Re-running `no-mistakes init` later preserves the stored fork URL unless you pass a new `--fork-url`.

Fork routing currently requires both `origin` and `--fork-url` to be GitHub remotes with owner/repo paths.
GitLab and Bitbucket fork MR/PR routing are not implemented yet; if a legacy or manually edited repo record has `fork_url` set for those providers, PR creation skips instead of opening an unsafe self PR.

## GitLab

Install the GitLab CLI and authenticate:

```sh
# macOS
brew install glab

# Linux
# see https://gitlab.com/gitlab-org/cli

glab auth login
```

**What you get:**

- PR (merge request) creation and update
- CI pipeline status polling until the merge request is merged, closed, or the configured `ci_timeout` idle window elapses
- Failed job trace fetching (`glab ci trace`) for the CI auto-fix step
- Merge-conflict polling and auto-fix, same as GitHub

## Bitbucket Cloud

Bitbucket Cloud uses the REST API directly rather than a provider CLI. Set two environment variables (and optionally a third):

```sh
export NO_MISTAKES_BITBUCKET_EMAIL=you@example.com
export NO_MISTAKES_BITBUCKET_API_TOKEN=your-api-token

# Optional: override the API base URL
export NO_MISTAKES_BITBUCKET_API_BASE_URL=https://api.bitbucket.org/2.0
```

Get an API token from [Bitbucket account settings](https://bitbucket.org/account/settings/app-passwords/).

**What you get:**

- PR creation and update
- CI pipeline status polling until the PR is merged, declined, or the configured `ci_timeout` idle window elapses
- Failed pipeline step log fetching for the CI auto-fix step

**What you don't get (yet):**

- PR mergeability polling
- Merge-conflict auto-fix

These are GitHub and GitLab only right now.

## Harness Code

`no-mistakes` talks to Harness Code two ways and picks the first one that works at runtime — no flag, no config:

1. **`harness` CLI** ([`harness/harness-unified-cli`](https://github.com/harness/harness-unified-cli)). If `harness` is on `PATH` and its active profile's account matches the repo's account, PR commands go through the CLI and pick up creds from `~/.harness/credentials` — no env vars needed.
2. **REST + `HARNESS_API_KEY`**. Used when the CLI isn't installed, or when the CLI profile points at a different account than the repo. The PR step skips with a one-line hint when neither is usable.

`git push` itself only needs your local git credentials — `HARNESS_API_KEY` and the CLI are both **optional** for pushing.

### Install the CLI (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/harness/harness-unified-cli/main/install.sh | sh
harness auth login
```

When the CLI is logged into a profile in the same account as the repo, the PR step uses it transparently. To target a non-default account for a specific repo, log into a named profile and export it for the daemon:

```sh
harness auth login --profile work
export HARNESS_PROFILE=work
```

### REST fallback (works without the CLI)

```sh
export HARNESS_API_KEY=pat.<accountId>.<rest>

# Optional overrides:
export HARNESS_ACCOUNT_ID=<accountId>          # required only if your key is not a "pat.<accountId>." token
export HARNESS_BASE_URL=https://app.harness.io # defaults to app.harness.io; override for dedicated tenants
export HARNESS_HOST=git.my-company.com         # classify a self-hosted host as Harness Code in DetectProvider
```

Supported clone/UI URL shapes:

- `https://git.harness.io/<account>/<org>/<project>/<repo>[.git]`
- `https://app.harness.io/gateway/code/git/<account>/<org>/<project>/<repo>[.git]`
- sharded SaaS hosts like `git0.harness.io` (UI links resolve to the matching `harness0.harness.io` UI host)
- any host matching `HARNESS_HOST` for self-hosted installs

`git push` uses your local git credentials and doesn't need `HARNESS_API_KEY`.

**What you get:**

- PR creation and update
- PR state (open/merged/closed) polling, so the rest of the pipeline knows when a PR has landed

**What you don't get (yet):**

- CI / check polling and the auto-fix loop
- Mergeability polling
- Merge-conflict auto-fix
- Fork PR routing (the underlying API supports `source_repo_ref`, but URL parsing + routing isn't wired end to end yet)

When `HARNESS_API_KEY` isn't set, the PR step skips and prints a clickable compare URL so you can open the PR by hand.

## Self-hosted GitHub/GitLab

Self-hosted GitHub Enterprise and self-hosted GitLab instances work through the same `gh` and `glab` CLIs. Authenticate the CLI against your instance (`gh auth login --hostname your-ghe.example.com`, `glab auth login --hostname gitlab.example.com`) and `no-mistakes` will route through the CLI as usual.

Self-hosted GitLab is detected out of the box even when the hostname carries no `gitlab` marker (for example `git.example.com`).
When the hostname is not obviously GitLab, `no-mistakes` consults glab's configured hosts (`config.yml`, honoring `GLAB_CONFIG_DIR` then `XDG_CONFIG_HOME/glab-cli`, then `~/.config/glab-cli`) and treats the upstream as GitLab if its host appears there as a configured host or `api_host`.
Running `glab auth login --hostname your-gitlab.example.com` is enough to make detection succeed; if glab is not configured for the host, detection fails closed and the upstream is treated as unsupported.

The GitLab backend is pinned against `glab v1.5x`. Self-hosted detection and the merge-request and CI steps rely on its current flag and API surface, so keep `glab` reasonably up to date.

## Unsupported hosts

If your upstream isn't GitHub, GitLab, Bitbucket Cloud, or Harness Code:

- The **push** step still runs - `no-mistakes` pushes through git to the configured target like any other remote.
- The **PR** step marks itself as `skipped`.
- The **CI** step marks itself as `skipped`.

Everything before push (rebase, review, test, document, lint) still works regardless of host. If your host has a CLI that exposes CI status and PR state, open an issue - new providers are straightforward to add.

## Checking what's wired up

```sh
no-mistakes doctor
```

`doctor` currently checks `gh` availability. For GitLab, confirm `glab` is installed and authenticated. For Bitbucket Cloud, confirm the two env vars are set in the environment the daemon runs under. For Harness Code, confirm `HARNESS_API_KEY` (and `HARNESS_ACCOUNT_ID` / `HARNESS_BASE_URL` / `HARNESS_HOST` as needed) are set in that same environment.

:::note
When the daemon runs through a managed service (launchd, systemd, Task Scheduler), it reloads environment from your login shell on macOS and Linux so `gh` auth and `NO_MISTAKES_BITBUCKET_*` vars are picked up, and it augments `PATH` with common binary directories. If credentials or PATH-derived tools are missing, check `~/.no-mistakes/logs/daemon.log` for a login-shell environment resolution warning. On Windows it reuses the current process environment.
:::
