This is the command for the local daemon

## Configuring it

`jmj` never writes its own config file. `-generate-config` prints a complete,
valid configuration to stdout and does nothing else — it creates nothing, moves
nothing, and reads no config file, so it needs no write permission anywhere.
You redirect the output to wherever you can write:

```sh
jmj -generate-config > ~/.config/jmj/config.json

# somewhere privileged? that is the shell's problem, not jmj's
jmj -generate-config | sudo tee /usr/local/etc/jmj.json
```

Flags fully determine the output; anything you leave out takes its default.
Re-running with the same flags produces the same file, so redirecting onto an
existing config is well defined.

```
-tracker   tracker URL                      (default http://127.0.0.1:8080)
-addr      listen address, host:port        (default 127.0.0.1:9001)
-buffer    daemon's temp buffer directory   (default ~/.cache/jmj)
-cache     pkg's package cache, read-only   (default /var/cache/pkg)
-config    which config file to run from    (default ~/.config/jmj/config.json)
```

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

At startup — not at generation time — the daemon checks the config against the
machine it is on: `buffer_dir` is created if absent and probed for writability,
and `cache_dir` must already exist, because the pkg cache is read-only to the
daemon and it will never create it.
