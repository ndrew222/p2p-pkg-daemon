# ADR-008: `fsnotify` triggers the repository-database reload

**Status:** Approved by Andrew (ruled 2026-08-09; drafted from that ruling)

Resolves the follow-up recorded at `docs/logs/HANDOFF.md` §5.2, which has been
open since the repository-database reader landed: `Repositories.Reload` exists
and is tested, and nothing calls it.

## Context

`internal/daemon/repodb.go` holds a **snapshot**, not a live query, and the
reason is in its own doc comment: neither half of `Repository` returns an error,
so a lookup that could fail would have to report "not found" for an I/O error,
and the facade would turn that into a `404` telling pkg this mirror does not
have a package it does have. The snapshot costs 6–12 MB for the ~38,000 rows
measured on the reference host and buys a truthful answer.

A snapshot goes stale. `pkg update` rewrites `<repo>/db` — the SQLite catalogue
under `repo_db_dir` — and every package added since the daemon started is then
absent from the daemon's copy. Today the only caller of `Reload` is the SIGHUP
path, and only when `repo_db_dir` itself changed (`daemon.go`), which is a
configuration change and not the event that matters.

**ADR-003 made this a broken install rather than a slow one.** Before it, a
facade `404` was believed to fall through to another mirror. §7.1 measured that
it does not: fall-through happens between mirrors within a repository, never
between repositories, so a `404` ends the install with exit 1. The reworked
facade narrows `404` to "provably absent from the repository database" — which
is exactly what a stale snapshot manufactures. The sequence is ordinary:

```
pkg update      # catalogue now lists foo-2.0
pkg install foo # daemon's snapshot predates it -> 404 -> install fails
```

There is no second source and no retry. So the trigger is not a refinement; it
is what keeps a long-running daemon usable at all.

A second, quieter consequence. The cache watcher's `SanityFilter` compares each
cached file's size against the repository database before announcing it, so a
file whose size disagrees is dropped from the announce list. After a catalogue
rewrite those comparisons are being made against superseded numbers, and a file
dropped under the old catalogue is never reconsidered — no cache event will fire
for a file that is already sitting there unchanged.

Options considered:

1. **Leave it.** The status quo: the snapshot is fixed for the process
   lifetime and the operator restarts the daemon after every `pkg update`. An
   undocumented manual step whose omission presents as a package manager that
   cannot find a package that exists.
2. **Reload periodically.** A timer either fires far more often than `pkg
   update` happens — reloading ~38,000 rows and reopening every catalogue for
   nothing — or leaves a window in which installs fail. Neither end of the
   trade is good, because the event being tracked is discrete and infrequent
   and a timer cannot be told about it.
3. **Reload lazily, on a repository-database miss.** Reactive and cheap-looking,
   and wrong twice over: it makes every genuine `404` pay for a full catalogue
   reload, and it does nothing for the announce list, which is the other reader
   of the snapshot and never misses in a way this would notice.
4. **Watch `repo_db_dir` with `fsnotify`.** Chosen.

## Decision

**The daemon watches `repo_db_dir` with `fsnotify` and reloads the repository
database when the catalogue changes.**

The mechanism follows the cache watcher (`internal/daemon/watcher.go`), which is
already the house pattern for this and already answers most of the questions
below the same way.

- **Directories are watched, not files.** The root of `repo_db_dir` and each
  repository subdirectory beneath it. Watching `<repo>/db` itself would cost a
  descriptor per catalogue and, on kqueue, would follow the *inode* — so a
  catalogue replaced by a rename would leave the watch pointing at a file
  nothing will ever write to again, silently, forever. A directory watch sees
  the rename.

- **Every reload re-walks and re-adds the watches.** A repository directory can
  appear (a repository is enabled), disappear, or be replaced wholesale. Rather
  than reason about which of those the platform reports as which event, the
  watch set is rebuilt from the directory tree each time — the same walk
  `Start` does. This is what makes the design correct on inotify and kqueue
  without depending on either one's rename semantics.

- **`repo_db_dir` is read-only and stays read-only.** It must already exist; a
  missing directory is refused, never created. This is the same hard constraint
  the cache watcher violated once with `MkdirAll` and it applies here with more
  force, because these are pkg's signed catalogues.

- **Events are coalesced behind a settle delay of two seconds.** A catalogue
  refresh is not one write: pkg stages files and moves them into place, and each
  step is one or more events. Reloading on the first of them would read a
  half-written catalogue and reloading on each of them would do the whole job
  several times over. Two seconds is chosen because the reload is expensive and
  the delay is free: the only way it could matter is if an install began inside
  the window, and `pkg install` cannot start before the `pkg update` that
  prompted the events has finished. The value is a constant with this reasoning
  attached, not a tuning parameter — there is no configuration key for it.

- **A failed reload is not fatal and must not discard the snapshot.** Failure at
  *startup* stays fatal, for the reason `openRepositoriesLocked` already gives:
  a daemon with no catalogue cannot verify a single package and would answer
  `404` to everything. Failure at *runtime* is different — the daemon has a
  working snapshot and the alternative to keeping it is having none. It logs and
  keeps what it has, and the next event tries again. `Reload` already builds its
  replacement maps in locals and returns before swapping, so this is a property
  to state and to test, not one to add.

- **A successful reload triggers a re-announce.** This is the fix for the
  `SanityFilter` staleness above: the keep-alive rescans the cache on every
  announce and re-applies the filter against the current snapshot, so nudging it
  is the whole of the remedy. A *failed* reload does not nudge — nothing about
  what this host can serve has changed.

## Consequences

**HANDOFF §5.2's follow-up closes, and the last "known to go stale" behaviour
in the daemon goes with it.** A daemon may now run across arbitrarily many
`pkg update` cycles.

**The daemon gains a third long-lived goroutine sharing its state**, alongside
the cache watcher and the keep-alive. §5.3 merged a data race that the project
gate could not see, because the gate does not run `-race`; the specific shape of
it was a watcher goroutine reading a `Daemon` field that shutdown cleared. This
watcher must take the same precaution the cache watcher now does — hold the
closure it was given rather than read a field — and `go test ./... -race
-count=2` is required before the merge, per §0.

**The watcher's lifetime is discovery's.** It is started and stopped with the
cache watcher and the keep-alive, because SIGHUP already restarts discovery on a
superset of the conditions that would stale this watcher — including a change to
`repo_db_dir`, which replaces the `Repositories` instance it reloads.

**A rewrite that produces no filesystem event produces no reload.** The design
assumes pkg touches `repo_db_dir` in a way the platform reports. That is nearly
certain and is not yet measured on FreeBSD; the measurement is part of the host
round, and if a catalogue refresh turns out to leave the watched directories
untouched, the mechanism section above is what has to change.

**A hostile local process can make the daemon reload repeatedly** by touching
`repo_db_dir`. It cannot make it reload faster than once per settle delay, and
anything that can write to pkg's repository directory can already replace the
catalogue itself — which is a considerably worse position than being able to
waste some of the daemon's CPU.
