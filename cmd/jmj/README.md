This is the command for the local daemon

## Configuring it

`jmj` never writes its own config file. `-generate-config` prints a complete,
valid configuration to stdout and does nothing else — it creates nothing, moves
nothing, and reads no config file, so it needs no write permission anywhere.
You redirect the output to wherever you can write:

```sh
jmj -generate-config -upstream 'https://pkg.FreeBSD.org/${ABI}/quarterly' > ~/.config/jmj/config.json

# somewhere privileged? that is the shell's problem, not jmj's
jmj -generate-config -upstream 'https://pkg.FreeBSD.org/${ABI}/quarterly' | sudo tee /usr/local/etc/jmj.json
```

Flags fully determine the output; anything you leave out takes its default.
Re-running with the same flags produces the same file, so redirecting onto an
existing config is well defined.

```
-upstream      REQUIRED. mirror the facade proxies to   (no default)
-tracker       tracker URL                        (default http://127.0.0.1:8080)
-facade-addr   where pkg reaches us, host:port    (default 127.0.0.1:9001)
-serving-addr  where peers reach us, host:port    (default 0.0.0.0:9002)
-temp-dir      scratch space for downloads        (default the OS temp directory)
-cache         pkg's package cache, read-only     (default /var/cache/pkg)
-repo-db       pkg's repository databases, r/o    (default /var/db/pkg/repos)
-config        which config file to run from      (default ~/.config/jmj/config.json)
```

`-upstream` is the one required flag, and the only setting with no default.
Without it `-generate-config` prints nothing and exits non-zero, so a redirect
leaves an empty file rather than a config that cannot start.

It has no default because it decides **which repository you install from**. The
daemon is pkg's only mirror and proxies the catalogue as well as the packages,
so pkg's own config points at loopback and says nothing about which real
repository sits behind it. Set `quarterly` when you meant `latest` and nothing
errors — pkg builds its database from whatever is proxied, every checksum
matches, and both branches carry the same signature. The only symptom is your
package versions being quietly wrong. That is not a setting to guess on your
behalf.

Quote it in the shell: `${ABI}` is expanded by **jmj**, not by your shell, and
only at startup on the machine that runs the daemon — so a config generated on
one box stays correct on another. jmj asks `pkg config abi` for the value, and
only ever runs that when the placeholder is actually present, which is why a
literal URL works fine on a machine with no pkg at all.

Paste `pkg+https://…` straight out of `/etc/pkg/FreeBSD.conf` and jmj will
reject it and print the corrected URL. `http://` is allowed but warned about:
tampering is still caught, since pkg checks the catalogue signature and jmj
checks package hashes, but the transfer is readable in transit.

The daemon listens on two ports, and they are not interchangeable:

- **`facade_addr`** is the mirror facade — the port `pkg` fetches from. It
  **must be a loopback address**, and the daemon refuses to start otherwise.
  This is not advisory: the facade fetches packages from the network on behalf
  of whoever asks, so reachable from off-host it is an open relay for someone
  else's bandwidth.
- **`serving_addr`** is where other daemons fetch packages *from* you. It is
  public by nature, and its port is the one announced to the tracker. Peers
  cannot reach a loopback address, so this one is normally left on all
  interfaces.

`temp_dir` is scratch space for in-flight downloads. A package is spooled there
while it is being verified and deleted as soon as it has been served — it is not
a cache and nothing in it is meant to survive. The default is the OS temp
directory for exactly that reason.

`repo_db_dir` is where pkg keeps its repository catalogues — one subdirectory
per configured repository, each holding a SQLite file named `db`. The daemon
reads every one of them to learn each package's expected SHA-256 and exact size:
that is the only source of truth for verification, since peers are not trusted
and the tracker never checks content. It is opened strictly read-only. Like
`cache_dir` it must already exist and is never created, and a daemon that cannot
read it will not start — without a catalogue it could verify nothing and would
answer 404 to every request.

`max_concurrent_seeds` and `max_concurrent_seeds_per_ip` bound how many peer
transfers this daemon serves at once — in total, and to any one remote IP. Both
default to `0`, meaning unlimited, which is the behaviour the daemon has always
had; set them only if you have a reason to. When either is full the seeder
answers `503` immediately: no queueing and no `Retry-After`, because the peer
asking has other holders to try and pkg's own mirror behind those, so refusing
fast is cheaper for them than waiting. The remote identity is the connection's
source address and never a header. A per-IP cap larger than the global one can
never fire, and jmj says so at startup rather than leaving you to wonder.

Configs written for older builds are rejected with a message naming the key:
`listen_addr` became `facade_addr` plus `serving_addr`, and `buffer_dir` became
`temp_dir`. The file is left alone so you can edit it.

Because generation touches no filesystem, you can generate a config on one
machine for another — the FreeBSD default `cache_dir` does not have to exist on
the box writing the file.

## Running it

```sh
jmj -config ~/.config/jmj/config.json
```

No flags are required. The daemon discovers what it can serve by watching
`cache_dir`; an empty cache is fine, it simply stays quiet until there is
something to announce. `SIGHUP` reloads the config without a restart.

To see it working — configured as a pkg repository, serving a real
`pkg install`, and fetching from a peer on another machine — follow
`docs/logs/claude-demo-guide.md` §2. Every command in it has been run.

At startup — not at generation time — the daemon checks the config against the
machine it is on: `temp_dir` is created if absent and probed for writability,
and `cache_dir` and `repo_db_dir` must already exist, because pkg's cache and
its repository catalogues are read-only to the daemon and it will never create
them. This is also where `${ABI}` in `upstream_url` is resolved, and where a
placeholder that cannot be resolved stops the daemon rather than being proxied
through as a literal. `SIGHUP` reload does the same, so a reloaded config can
never keep an unexpanded placeholder.
