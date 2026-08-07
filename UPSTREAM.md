# Relationship to upstream

This is a fork of [eduardolat/pgbackweb](https://github.com/eduardolat/pgbackweb).
It carries fixes that are not upstream yet. The intent is still to contribute them
back, but see "State of upstream" below before planning around that: the evidence
says this divergence outlives any reasonable wait, and it should be maintained as
though it were permanent.

This file records what diverges, why, and what still has to happen before each
piece is proposed upstream. Keep it current when the answers change.

## Why the fork exists

Backups were leaving `pg_dump` processes running indefinitely, each holding a
`REPEATABLE READ` transaction open on the database it had been backing up. Those
transactions pinned the xmin horizon, so autovacuum could not reclaim dead
tuples, and the resulting bloat showed up as steadily rising CPU on the database
host.

The upstream issues are open with no fix:

- [#165](https://github.com/eduardolat/pgbackweb/issues/165) — backups to S3 hang
  indefinitely, blocking autovacuum.
- [#91](https://github.com/eduardolat/pgbackweb/issues/91) — a lot of idle
  connections after a fast backup.

## What diverges

Both changes below are merged into this fork's `main` and shipping in the published
image. Neither is upstream, and neither has been proposed there yet.

**Orphaned `pg_dump` processes and stalled uploads** — addresses `#165`.

`Dump` returned an `io.Reader` that nobody closed, so any early return abandoned
the stream and left `pg_dump` blocked forever on a full pipe. The dump is now an
`io.ReadCloser` whose `Close` terminates the process, a watchdog abandons a
backup whose destination stops consuming it, context reaches the storage layer,
connectivity checks and uploads are bounded, and a failed multipart upload is
cleaned up exactly once.

Note that TCP keepalives do not help here: once the client stops reading, the
connection enters TCP zero-window state, where keepalives are not sent.

**libpq keepalives and `connect_timeout`** — addresses `#91`.

Injects keepalive parameters into the connection strings handed to `psql` and
`pg_dump`, covering the opposite failure: a client waiting on a server that has
gone away, where releasing the consumer does nothing.

This one is deliberately slower to land. It rewrites user-supplied connection
strings, which means reimplementing libpq's own parsing rules — service files,
environment fallbacks, percent-encoded names, quoting and escapes, and both the
URI and keyword/value formats. Review has repeatedly found valid connection
strings that a careless edit would break, and breaking a working connection is
worse than the problem being fixed.

## Published images

| Fork version   | Upstream base | Commit    | Image digest                                                              |
| -------------- | ------------- | --------- | ------------------------------------------------------------------------- |
| `v0.5.1-0.1.0` | `v0.5.1`      | `e88cc77` | `sha256:5b5f9003278ae509f78f7d5a3b8e5e136a24c864e5b56242056b1f21da9b364c` |

Two tags per release, both multi-arch (`linux/amd64` + `linux/arm64`):

- `tuanmedeiros/pgbackweb:0.5.1-0.1.0` — the one a compose file points at
- `tuanmedeiros/pgbackweb:0.5.1-0.1.0-e88cc77` — records which commit was built

Neither is a guarantee of content. Both are tags, and a tag is a name that anyone
holding a push credential can repoint — including by pushing straight to the
registry, which never reaches the guard in `publish-fork-image.yaml`. The
`<version>-<commit>` form is a label saying which commit produced an image, not
proof of what that image holds. Roll back by digest when it has to be certain.

`latest` is never published. The version scheme is `<upstream-version>-<fork-version>`;
note that semver reads anything after a hyphen as a prerelease, so `0.5.1-0.1.0`
sorts _before_ upstream `0.5.1`. Harmless while images are pinned by hand, but an
auto-updater comparing versions would "upgrade" back to upstream and silently drop
these fixes.

The digest is recorded because it is the only one of the three that cannot be
reused for different content. To check what a running container actually is:

```sh
docker inspect <container> --format '{{.Image}}' \
  | xargs docker image inspect \
      --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}no registry digest — built locally, not pulled{{end}}'
```

For an image pulled from the registry that prints
`tuanmedeiros/pgbackweb@sha256:...`, which should match the table above.

An image built on the machine and never pushed has no `RepoDigests` at all, which
is why the command says so rather than indexing an empty list and failing with a
template error. That answer is worth having: an image with no registry digest is
not one of these releases, whatever its tag says.

`docker inspect <container> --format '{{.Config.Image}}'` is shorter and also
carries the digest — but only under Swarm, which resolves the tag and records
`image:tag@sha256:...` in the service spec when it deploys. Started from a plain
Compose file the same command returns the bare tag, and there is nothing to
compare.

## State of upstream

Measured 2026-08-07:

| Signal                    | Value                                                                                       |
| ------------------------- | ------------------------------------------------------------------------------------------- |
| Last merge of actual code | PR #143, 2025-10-07 — ten months                                                            |
| Last commit on `develop`  | 2025-11-21                                                                                  |
| Open pull requests        | 14, the oldest from August 2024                                                             |
| Issue #165                | opened 2025-12-28, two comments — a bot and another affected user. None from the maintainer |
| Issue #91                 | opened 2025-02-07, zero comments in eighteen months                                         |

The only 2025-11-21 merge was a sponsor line in the README, not code.

There is prior art worth knowing about: **PR #175** (`ambyte`, 2026-06-05) attacks
`#165` from the same direction — `Dump` returning an `io.ReadCloser` whose `Close`
cancels the process. It touches the same three files this fork does. It is missing
the part that _triggers_ the close when a destination goes silent rather than
erroring, which is what `streamutil.StallReader` provides here. Its only review in
two months came from a bot.

Practical consequence: do not plan on merging upstream fixes back down, and do not
plan on these changes landing upstream on any schedule. If the repository wakes up,
that is upside, not the plan.

## Before proposing upstream

- [x] **Validate against a real PostgreSQL and a real S3 endpoint.** Done
      2026-08-07 — see "How the fix was validated" below.
- [x] **Make the timeouts configurable.** Hardcoded constants would be the first
      thing a maintainer asks about, since the right values depend on database
      size and link speed. See the `PBW_*_TIMEOUT` variables in the README.
- [x] **Ask on the issues before opening a pull request.** Done 2026-08-07:
      comments on [#165](https://github.com/eduardolat/pgbackweb/issues/165),
      [#91](https://github.com/eduardolat/pgbackweb/issues/91) and
      [#175](https://github.com/eduardolat/pgbackweb/pull/175), carrying the
      reproduction and the measurements, and offering the watchdog to #175 rather
      than opening a competing pull request. No reply yet.
- [ ] ~~**Run in a non-production environment first.**~~ Not done, and now moot:
      the image went straight to production on 2026-08-06 and has been taking
      hourly backups since. Recording it rather than quietly dropping it — the
      soak happened, in the wrong place. Do it the other way round next time.
- [ ] **Split into two pull requests before proposing.** One per issue. The
      combined diff is ~2700 lines across 30 files, which is not reviewable, and
      the two changes carry different risk: the `#165` work is contained, while the
      `#91` work rewrites user-supplied connection strings. `CONTRIBUTING.md`
      requires branching from and targeting **`develop`**, not `main` — this fork
      is based on `main`, so both branches need rebasing first. Exclude the
      fork-only files (`publish-fork-image.yaml`, `UPSTREAM.md`, `README.md`,
      `.gitignore`).

## How the fix was validated

Production, 2026-08-07. Source database on a separate host from the pgbackweb
instance, destination a real S3-compatible endpoint.

**Killing the destination does not reproduce the bug.** It sends a connection
reset, which is a hard error the code already handled — the backup failed ~25s
later and cleaned up. The bug needs the destination to go _silent_: connection
open, nobody reading. `docker pause` produces exactly that, and as a bonus the
container never exits, so an orchestrator does not restart it out from under the
test.

With `PBW_BACKUP_STALL_TIMEOUT=2m`:

```
17:09:59  pg_dump starts
17:11:33  docker pause on the destination
~17:12:18 in-flight multipart parts finish draining
17:14:18  backup abandoned, pg_dump terminated
```

2m45s from pause to abandon — ~45s of residual drain, then the configured 2m of
true silence. The watchdog measures lack of progress, not elapsed time, and the
numbers show it.

On the source database afterwards:

```sql
SELECT count(*) FILTER (WHERE backend_xmin IS NOT NULL),
       count(*) FILTER (WHERE state = 'idle in transaction'),
       max(age(backend_xmin))
FROM pg_stat_activity WHERE datname = '<source_db>';
-- 0 | 0 | null
```

Before the fix that connection stayed `idle in transaction` indefinitely, and every
backup cycle left another one behind.

To repeat this, set `PBW_BACKUP_STALL_TIMEOUT` low so the wait is bearable, and put
it back to `30m` afterwards — 2m is aggressive enough in production to abandon a
legitimately slow upload.

## Keeping in sync

Upstream is not configured as a git remote by default. To compare:

```sh
git remote add upstream https://github.com/eduardolat/pgbackweb.git
git fetch upstream
git log --oneline upstream/main..main   # what this fork adds
git log --oneline main..upstream/main   # what this fork is missing
```
