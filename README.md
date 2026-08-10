# p2p-pkg-daemon

Peer-to-peer package distribution for FreeBSD, alongside `pkg` rather than
inside it.

A host that has already downloaded a package can hand it to the host next to it
instead of both fetching it from a mirror on the other side of the world. The
awkward part is doing that without touching the package manager: **`pkg` is
never modified, wrapped or patched.** The daemon integrates the only way that
leaves `pkg` alone — by impersonating a mirror over HTTP, on loopback, and
answering the requests `pkg` was going to make anyway.

Everything `pkg` is handed is verified against `pkg`'s own signed repository
catalogue before it is served, and `pkg` re-verifies it afterwards. Peers are
not trusted, and the design assumes some of them are lying.

> Orientation only. The contracts live in `docs/`, the rules for changing
> anything live in `AGENTS.md`, and where either disagrees with this file, this
> file is the bug.

## Two binaries

| | | |
|---|---|---|
| `cmd/trac` | the tracker — **one per swarm** | A directory, and nothing else. It answers *who claims to hold this package?* It never relays package bytes and never verifies content. In-memory, no state on disk, runs on any OS. |
| `cmd/jmj` | the daemon — **one per host** | Fronts `pkg` as a mirror, fetches from peers, seeds to peers, and tells the tracker what this host can serve. |

```sh
go run ./cmd/trac                       # tracker on :8080

go run ./cmd/jmj -generate-config -upstream 'https://pkg.FreeBSD.org/${ABI}/quarterly' \
  > ~/.config/jmj/config.json           # prints to stdout; the shell does the writing
go run ./cmd/jmj -config ~/.config/jmj/config.json    # SIGHUP reloads
```

`-upstream` is the one required setting and has no default, because it decides
*which repository you install from* — see `cmd/jmj/README.md` for the full flag
list and `cmd/trac/README.md` for the tracker's surface.

## How a package moves

`pkg` asks the facade for a package (loopback only) → the daemon asks the
tracker who holds it → it fetches from a peer, spooling through `temp_dir` and
hashing as the bytes arrive → it checks the result against the expected SHA-256
and the exact size **from `pkg`'s own repository database** → it serves the
bytes and deletes the spool.

If no peer can supply it — nobody has it yet, the tracker is unreachable, every
holder failed — the daemon fetches from the configured upstream mirror and
streams it straight through. `pkg` cannot tell the difference, which is the
point: it has one mirror, and an error from it would end the install rather than
redirect it. Repository metadata is relayed from the same upstream, unmodified
and uncached.

Meanwhile a watcher on the `pkg` cache tells the tracker what this host can
serve, so a package this host just installed becomes available to everyone else
without anyone doing anything.

Neither end of a transfer ever holds a package in memory: the sender streams
from an open file handle and the receiver streams to a temporary file. Measured,
moving 98 MB between two machines changed the receiving daemon's resident memory
by 20 KiB.

## Three wires that share no path grammar

| Wire | Surface |
|---|---|
| daemon ↔ tracker | `POST /announce`, `POST /ping`, `GET /peers?pkg=<name-version>` |
| pkg → daemon (facade) | `…/All/[Hashed/]<name-version>[~hash10].pkg`, plus every non-package path |
| daemon ↔ daemon (peer) | `GET /pkg/<name-version>` |

The peer namespace is deliberately unlike the facade's, so that a seeding daemon
cannot be mistaken for — or used as — a `pkg` mirror. Unifying them is a
standing "do not".

## Build and test

```sh
go build ./... && go vet ./... && go test ./...
```

No CI exists; that, plus `gofmt`, is the gate. The suite needs no FreeBSD, no
`pkg` and no second machine. `TESTING.md` is the map of what is tested at which
layer — and of what is not.

Want to see it work? `docs/logs/claude-demo-guide.md` is every demo in the
project ordered by what it costs to run: five seconds and no dependencies at one
end, three machines at the other.

## Where to look next

| | |
|---|---|
| `AGENTS.md` | Ground rules, document precedence, hard constraints. Binding. |
| `docs/adr/` | Architectural design records — the highest-precedence documents here |
| `docs/use-case-descriptions.md`, `docs/uc-*.puml` | What the system does, UC-01 … UC-07 |
| `docs/tracker-protocol-spec-v0.2.md`, `docs/protocol-spec-v0.1.md` | The tracker wire, and its semantics |
| `docs/peer-transfer-spec-v0.2.md` | The daemon↔daemon wire |
| `docs/logs/HANDOFF.md` | Current state: what is done, what is open, what is next |
| `TESTING.md` | How it is tested |

Go 1.26, module `github.com/ndrew222/p2p-pkg-daemon`. The pkg→daemon wire has
no spec file and is governed by ADRs; `docs/mirror-facade-spec-v0.1.md` is
deprecated history and is not a contract.
