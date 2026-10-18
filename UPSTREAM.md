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

## Upstream base and security

Since `0.5.2-0.4.0` the fork is based on upstream `v0.5.2`; before that it was
`v0.5.1`. Upstream `v0.5.2` was merged whole, and the only part the fork did not
already have is a security fix:

**In `v0.5.1`, `POST /auth/create-first-user` never checked whether a user already
existed.** Anyone who could reach an instance could create an account on it and
read every PostgreSQL connection string and S3 key stored there. Every fork image
up to `0.5.1-0.3.0` has this hole; upgrade to `0.5.2-0.4.0` or later. Upstream
fixed it in `0ad8744` (PR [#180](https://github.com/eduardolat/pgbackweb/pull/180)):
the endpoint refuses once a user exists, logs the attempt and redirects to the
login page, it is rate limited, a unique index allows a single user, and pages are
sent with `noindex`.

On start, the migration that adds that index refuses to run when it finds more
than one user, with a security alert saying how to clean up. The app does not start
until the extra users are deleted and the stored credentials rotated.

**The fork runs that migration under a different name.** Upstream ships it as
`20260828054515`, which is older than the fork's own `20260925000001`. goose
refuses an unapplied migration older than the newest applied one, so under the
upstream name a database already on the fork fails with "found 1 missing
migrations" and the app does not start. The fork runs the same migration as
`20261018000001`, with `CREATE UNIQUE INDEX IF NOT EXISTS`, so it also applies on a
database coming from upstream `v0.5.2`, where `20260828054515` already created the
index. Tested with goose against PostgreSQL 16 on 2026-10-18:

| Database before `goose up`    | Result                                          |
| ----------------------------- | ----------------------------------------------- |
| new                           | ok, index created                               |
| fork `0.5.1-0.3.0`, one user  | ok, index created (fails under upstream's name) |
| upstream `v0.5.2`, one user   | ok, index kept                                  |
| fork `0.5.1-0.3.0`, two users | fails with the security alert, no index         |

A database from upstream `v0.5.2` keeps the row for `20260828054515` in
`goose_db_version`; goose ignores it.

## What diverges

The changes below are merged into this fork's `main`. The first two ship since
`0.5.1-0.1.0`; the next two ship since `0.5.1-0.2.0`; the last one ships since
`0.5.1-0.3.0`. None is upstream. Only the retention cleanup fix has been offered
there, on 2026-09-29 in a comment on `#50`; the others have not been proposed yet.

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

**A minimum number of copies that retention keeps** — addresses this fork's
[#11](https://github.com/tuanmedeiros/pgbackweb-eduardolat/issues/11). Upstream
asks for the same in [#121](https://github.com/eduardolat/pgbackweb/issues/121),
where the maintainer said on 2025-05-26 that it would be added, and more broadly in
[#147](https://github.com/eduardolat/pgbackweb/issues/147).

Retention deleted by age alone. A backup that kept failing for longer than its
retention period lost every copy, the last good one included, at the moment it was
needed most. Each backup task now has a `min_copies` setting, editable in the backup form: its
newest that many successful executions are never deleted by retention, however
old. Failed executions are still removed by age. It defaults to 0, which keeps
upstream's behaviour until the user opts in, so upgrading changes nothing.

It adds a column, `backups.min_copies`, in migration `20260925000001`. If upstream
ships its own answer to `#121`, the two schemas will not merge on their own:
reconcile them before taking upstream's migration instead of carrying both.

**Retention cleanup that carries on past a failure** — addresses this fork's
[#16](https://github.com/tuanmedeiros/pgbackweb-eduardolat/issues/16). Upstream
users report the same in [#50](https://github.com/eduardolat/pgbackweb/issues/50),
open since 2024-10-08. The maintainer said "let me take a look" on 2025-02-06, and
there is still no fix and no pull request.

The cleanup job gave up on the whole list at the first execution it could not
delete, so one broken destination, or one local backup whose file was gone, stopped
retention for every backup task. It now carries on, and skips the rest of a task
that failed until the next run, 10 minutes later. Each failure is logged with
`execution_id` and `backup_id`, and the run ends with the `deleted`, `failed` and
`skipped` counts. `LocalDelete` now treats a file that does not exist as deleted,
the way S3 treats a missing key, so such an execution can also be deleted by hand
in the dashboard again.

The loop comes from upstream (2024-07-21) and is unchanged on `upstream/main` and
`upstream/develop` as of 2026-09-29. There is no SQL or migration, so the change
stands on its own and does not depend on `min_copies`. `#50` also reports empty
date directories left behind after local files are deleted. This fork does not
change that.

Offered upstream on 2026-09-29, in a
[comment on #50](https://github.com/eduardolat/pgbackweb/issues/50#issuecomment-5894790353)
with the root cause, the fix and how it was tested, asking before opening a pull
request against `develop`. No reply yet.

**S3 endpoints must carry a scheme** — addresses this fork's
[#10](https://github.com/tuanmedeiros/pgbackweb-eduardolat/issues/10). Upstream
users hit the same in [#97](https://github.com/eduardolat/pgbackweb/issues/97),
[#98](https://github.com/eduardolat/pgbackweb/issues/98) and
[#117](https://github.com/eduardolat/pgbackweb/issues/117); each time the
maintainer answered "add `https://`" and the placeholder stayed the same.

The endpoint placeholder was `s3-us-west-1.amazonaws.com`, but the S3 client uses
the endpoint as given, and one without `http://` or `https://` never connects: the
SDK either cannot parse it or retries three times and reports `unsupported protocol
scheme ""`. The placeholder is now `https://s3.us-west-1.amazonaws.com`, the field
says the scheme is required, and creating, editing or testing a destination
rejects an endpoint without one, with a message saying what is missing.

Only the form changed, through a new `validate.S3Endpoint`; `s3.go` is untouched.
A destination saved without a scheme keeps failing as before until it is edited.
Completing `https://` in the S3 client was considered and rejected: the stored
endpoint would differ from the one used, and an HTTP-only MinIO would fail with a
different error instead of a clear one.

## Published images

| Fork version   | Upstream base | Commit    | Image digest                                                              |
| -------------- | ------------- | --------- | ------------------------------------------------------------------------- |
| `v0.5.1-0.1.0` | `v0.5.1`      | `e88cc77` | `sha256:5b5f9003278ae509f78f7d5a3b8e5e136a24c864e5b56242056b1f21da9b364c` |
| `v0.5.1-0.2.0` | `v0.5.1`      | `8d8fa58` | `sha256:502b3105e6aee45027c67b1ec670d274fe71353628da06b3f8e3e6ef81b6ad44` |
| `v0.5.1-0.3.0` | `v0.5.1`      | `67e46f0` | `sha256:f544747179846fb1547aba2018eda8e5541058bb764c9ad03359720387168eb2` |
| `v0.5.2-0.4.0` | `v0.5.2`      | `f08419f` | `sha256:0f7e09e7dba6bb3d036e40a976450ffeea3780efec68673dfba2fba78c1a8594` |

Two tags per release, both multi-arch (`linux/amd64` + `linux/arm64`):

- `tuanmedeiros/pgbackweb:0.5.2-0.4.0` — the one a compose file points at
- `tuanmedeiros/pgbackweb:0.5.2-0.4.0-f08419f` — records which commit was built

Neither is a guarantee of content. Both are tags, and a tag is a name that anyone
holding a push credential can repoint — including by pushing straight to the
registry, which never reaches the guard in `publish-fork-image.yaml`. The
`<version>-<commit>` form is a label saying which commit produced an image, not
proof of what that image holds. Roll back by digest when it has to be certain.

`latest` is never published. The version scheme is `<upstream-version>-<fork-version>`;
the fork number keeps counting when the upstream base changes, so `0.5.1-0.3.0` is
followed by `0.5.2-0.4.0`. Note that semver reads anything after a hyphen as a
prerelease, so `0.5.2-0.4.0` sorts _before_ upstream `0.5.2`. Harmless while images are pinned by hand, but an
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
