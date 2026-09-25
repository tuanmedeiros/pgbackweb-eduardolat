---
name: ci-local
description: Run this repo's lint, tests and build inside the CI dev image (docker/Dockerfile.dev). Use to verify a change before committing or opening a PR, after editing SQL or migrations, or when asked to run the tests, lint, or CI locally.
---

Runs what CI runs, in the same image, against the working tree. Host tooling is incomplete: `sqlc`, `goose` and `task` exist only in the image.

`$ARGUMENTS` optionally names task targets (for example `test` or `lint test`). With no arguments, run `lint test build`.

## 1. Build the image

From the repo root:

```sh
docker build -f docker/Dockerfile.dev -t pgbackweb-dev:local .
```

The first build installs PostgreSQL clients 13–18 and takes several minutes. Later builds are cached until `docker/Dockerfile.dev` or `.devcontainer/.bashrc` changes. Done when the command exits 0.

## 2. Run the targets

```sh
docker run --rm \
  -v "$PWD":/app \
  -v pgbackweb-dev-node-modules:/app/node_modules \
  -v pgbackweb-dev-cache:/root/.cache \
  -v pgbackweb-dev-gomod:/root/go/pkg/mod \
  -w /app pgbackweb-dev:local \
  bash -c 'set -e; task deps; for t in <targets>; do task "$t"; done'
```

Replace `<targets>` with the targets from `$ARGUMENTS`, or `lint test build`.

Why it is shaped this way:

- **`node_modules` is a named volume.** `task deps` runs `npm install`. Without the volume, that would install Linux builds of packages such as esbuild into the host's `node_modules`, over the macOS ones.
- **The targets run one by one instead of `task ci`.** `task ci` starts with `fixperms`, which runs `chmod -R 777 /app`, and `/app` is the mounted repo.
- **The cache volumes** keep Go modules, the Go build cache and the golangci-lint cache between runs.

Side effects on the host, all gitignored: `internal/database/dbgen/` is regenerated (this refreshes the stale host copy), `internal/view/static/build/` is rebuilt, and `dist/` gets Linux binaries.

## 3. Report

Done when every target exits 0. Report each target's result.

When a target fails, quote the failing output and fix the cause. If `prettier --check` fails, format with the repo's Prettier (`npx prettier --write <file>`). If `go fmt` would change files, run `gofmt -w <file>`. Then re-run only the target that failed.
