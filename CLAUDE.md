# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

## This is a fork

`origin` is `tuanmedeiros/pgbackweb-eduardolat`, a fork carrying fixes that are not upstream yet. `upstream` is `eduardolat/pgbackweb`. `UPSTREAM.md` records what diverges, why, the published images, and the plan for proposing each change upstream. Read it before changing anything it lists.

- Branch from `main` and open PRs against the fork's `main`. The `develop` flow in `CONTRIBUTING.md` is upstream's process. Use it only when preparing a PR for upstream.
- Pass `--repo tuanmedeiros/pgbackweb-eduardolat` to every `gh` command. With no default repo set, `gh` resolves to upstream, so a bare `gh pr create` opens the PR on `eduardolat/pgbackweb`.
- Upstream is read-only unless asked. Fetching and reading its issues and PRs is fine. Pushing, opening PRs or commenting there needs an explicit request.
- Update `UPSTREAM.md` in the same PR when a change alters what diverges, the published images, or the state of an upstream proposal.

## Branches and commits

- Branch prefixes are `feat/`, `fix/`, `chore/` and `ci/`.
- The commit subject is one plain sentence about the behaviour that changes, with no type prefix. Examples: "Stop a test filling memory with output nobody reads", "Make the operation timeouts configurable".
- The body explains why and includes the evidence: measurements, the failing case, what review found. `git log` shows the style.

## Verifying changes

Check changes with `/ci-local`, which runs lint, tests and build inside `docker/Dockerfile.dev`, the image CI uses. Host results can't be trusted: `sqlc`, `goose` and `task` exist only in the image, and `internal/database/dbgen/` is gitignored. The host's generated code is whatever was last generated, so after any edit to `internal/service/**/*.sql` or a migration, a host `go build` compiles against stale queries.

When the repo is bind-mounted into a container, run the individual task targets rather than `task ci`. `task ci` starts with `fixperms`, which runs `chmod -R 777 /app`, and `/app` is the mounted repo.
