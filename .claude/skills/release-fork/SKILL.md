---
name: release-fork
description: Publish a fork image to Docker Hub and record its digest in UPSTREAM.md and the README. Usage /release-fork <upstream-version>-<fork-version>
disable-model-invocation: true
---

Publishes `tuanmedeiros/pgbackweb:$ARGUMENTS` from the fork's `main` with `.github/workflows/publish-fork-image.yaml`, then records the release. Every `gh` call takes `--repo tuanmedeiros/pgbackweb-eduardolat`. Without it, `gh` resolves to upstream.

## 1. Pin and check

- `$ARGUMENTS` must match `<upstream-version>-<fork-version>` with no leading `v`, e.g. `0.5.1-0.2.0`. The workflow rejects anything else. If it doesn't match, stop and ask.
- Run `git fetch origin` and pin the commit: `SHA=$(git rev-parse origin/main)`.
- Check that CI passed on that commit: `gh run list --repo tuanmedeiros/pgbackweb-eduardolat --workflow ci.yaml --commit "$SHA"`. If it's missing, failed or still running, stop and report.
- Check the tag is unused: `docker buildx imagetools inspect tuanmedeiros/pgbackweb:$ARGUMENTS` should fail with "not found". The workflow refuses an existing tag unless `overwrite=true`, and overwrite moves a tag a deployed compose file may point at.

Done when the version is valid, the commit is pinned, CI is green on it, and the tag is free. Show the user the version and `SHA`, and wait for a go-ahead before dispatching.

## 2. Dispatch and watch

```sh
gh workflow run publish-fork-image.yaml --repo tuanmedeiros/pgbackweb-eduardolat \
  -f commit="$SHA" -f tag="$ARGUMENTS" -f image=tuanmedeiros/pgbackweb
```

Leave `overwrite` at its default. Find the new run with `gh run list --repo tuanmedeiros/pgbackweb-eduardolat --workflow publish-fork-image.yaml --limit 1` and follow it with `gh run watch <id> --repo tuanmedeiros/pgbackweb-eduardolat --exit-status`. Done when the run succeeds. If it fails, quote the failing step's log (`gh run view <id> --log-failed`) and stop.

## 3. Read the digest

```sh
docker buildx imagetools inspect tuanmedeiros/pgbackweb:$ARGUMENTS
```

Take the top-level `Digest:`, which is the multi-arch index covering amd64 and arm64. Find the short SHA the workflow used: it's in the run's summary as the `<tag>-<sha>` tag. Confirm `tuanmedeiros/pgbackweb:$ARGUMENTS-<short>` returns the same digest. Done when both tags resolve to one digest.

## 4. Record the release

On a branch `chore/release-$ARGUMENTS` from `origin/main`:

- `UPSTREAM.md`, "Published images": add a row `` `v$ARGUMENTS` | `v<upstream-version>` | `<short>` | `<digest>` ``. Update the two example tags below the table to the new release.
- `README.md`: set the compose example's `image:` to `tuanmedeiros/pgbackweb:$ARGUMENTS`.
- If the release changes what diverges from upstream, update that section too.
- Run Prettier over both files. It realigns the table, and `task lint` fails without it.

Commit with a plain-sentence subject, for example "Record the $ARGUMENTS image and point the README at it". Open the PR against the fork's `main` with `--repo tuanmedeiros/pgbackweb-eduardolat`. Done when the PR is open. Report the PR URL, the digest, and both tags.
