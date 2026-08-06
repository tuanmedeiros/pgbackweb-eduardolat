# Relationship to upstream

This is a fork of [eduardolat/pgbackweb](https://github.com/eduardolat/pgbackweb).
It carries fixes that are not upstream yet, and the intent is to contribute them
back rather than to maintain a permanent divergence.

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

### Merged into this fork

**Orphaned `pg_dump` processes and stalled uploads** — addresses `#165`.

`Dump` returned an `io.Reader` that nobody closed, so any early return abandoned
the stream and left `pg_dump` blocked forever on a full pipe. The dump is now an
`io.ReadCloser` whose `Close` terminates the process, a watchdog abandons a
backup whose destination stops consuming it, context reaches the storage layer,
connectivity checks and uploads are bounded, and a failed multipart upload is
cleaned up exactly once.

Note that TCP keepalives do not help here: once the client stops reading, the
connection enters TCP zero-window state, where keepalives are not sent.

### Open, not merged

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

## Before proposing upstream

- [ ] **Validate against a real PostgreSQL and a real S3 endpoint.** The tests
      use stand-in processes and fake endpoints, which prove the logic but not
      the integration. Reproduce the original failure — start a backup, stall the
      upload, confirm the connection on the source database goes away — before
      claiming the fix works.
- [ ] **Run in a non-production environment first.** Build an image from this
      fork and let it take real backups on a schedule before it replaces the
      upstream image anywhere that matters.
- [x] **Make the timeouts configurable.** Hardcoded constants would be the first
      thing a maintainer asks about, since the right values depend on database
      size and link speed. See the `PBW_*_TIMEOUT` variables in the README.
- [ ] **Ask on the issues before opening a pull request.** Both issues have been
      open a long time with no maintainer response, so it is worth confirming
      there is appetite for the change, and agreeing on the shape of it, before
      investing in review.

## Keeping in sync

Upstream is not configured as a git remote by default. To compare:

```sh
git remote add upstream https://github.com/eduardolat/pgbackweb.git
git fetch upstream
git log --oneline upstream/main..main   # what this fork adds
git log --oneline main..upstream/main   # what this fork is missing
```
