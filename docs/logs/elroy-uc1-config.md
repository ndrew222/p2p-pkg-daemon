# UC-1 Configuration System - Work Log

- **Author**: Elroy Tan
- **Date**: 2026-07-29
- **Branch**: feature/uc1-config
- **Status**: Implementation Complete (Testing Pending) 

## Overview
Implemented the configuration system for the `jmj` daemon (UC-1). The system allows users to configure the daemon via CLI flags, with persistent storage in a JSON config file.

## Thought Process: How I understand and tackle the Feature

When I first read UC-1, I interpreted "user configures the daemon via CLI" broadly. I designed an interactive menu system with 4 options:
1. Use default settings
2. Enter custom settings
3. Continue with existing config
4. Edit existing config

My reasoning was:
- **User experience**: Interactive setup is more beginner-friendly (like `mysql_secure_installation` or `ssh-keygen`)
- **Reduced cognitive load**: Users don't need to memorize flags
- **Guided workflow**: Users see their options clearly

### The Feedback That Changed My Approach

**Andrew's feedback** (21.07.2026):
- *"Your procedure flow is… a bit complicated. It's a lot of branching."*
- *"Also, making it interactive is too much. Basically, user calls jmj with flags like -p[ort] and -t[racker-address] and then if it works, it saves it to config file."*
- *"Just no. It's not worth it."*
- *"Did you check the sequence diagram when you wrote that?"*

**Andrew's key insight**: The sequence diagram shows `User -> Daemon : configure(trackerAddr, storageDir, port, ...)` as a **function call with arguments**, not an interactive wizard. This implies configuration should be passed as parameters, not prompted for.

### Revised Approach: Generator + Pipes Pattern

After Andrew's feedback, I pivoted to the **flag-based generator pattern** inspired by `wpa_supplicant`:

`jmj --generate-config -tracker <url> -addr <host:port> | sudo tee /etc/jmj/config.json`

`jmj -id <peer-id> -cids <hashes> -config /etc/jmj/config.json`

**Why this is better**:
1. Matches the sequence diagram exactly
2. Enables headless/automated operation (scripts can run without human input)
3. Follows Unix philosophy (composable with pipes, `jq`, `tee`)
4. Simpler to implement (no state machine, no stdin handling)
5. Separates config generation from daemon execution

**Summery of why it is better**
- **Approach**: `--generate-config` flag outputs JSON to stdout
- **Usage**: `./jmj --generate-config > ~/.config/jmj/config.json`
- **Rationale**: Follows Unix philosophy (inspired by `wpa_supplicant`). Separates config generation from daemon execution. Composable with tools like `jq` and `tee`.

## Design Decisions & Uncertainties Resolved

### Decision 1: Config File Path

| Uncertainty | How I Resolved It |
|-------------|-------------------|
| Should config be in `/etc/jmj/config.json` (system-wide) or `~/.config/jmj/config.json` (user-level)? | I initially assumed `/etc/jmj/` was a local folder in the project. Andrew clarified that `/etc` is the system-wide directory and requires root access. We decided to default to `~/.config/jmj/config.json` (XDG standard) for development, with `-config` override for system-wide use. |

**Who I clarified with**: Andrew (21.07.2026)
- Andrew: *"/etc/jmj/config.json is the absolute path. /etc is the usual place you put configuration files in a UNIX system."*
- Elroy: *"Then the user would need to be with root access I assume"*
- Andrew: *"Yup"*

**Outcome**: Default to `~/.config/jmj/config.json` (user-writable, no `sudo` needed). Support `-config` for system-wide override.

### Decision 2: Interactive Menu vs. Flags/Generator (This is mention above)

| Uncertainty | How I Resolved It |
|-------------|-------------------|
| Should the daemon have an interactive configuration wizard? | I designed a 4-option TUI. Andrew strongly rejected this as "too complicated" and "not worth it," pointing out that the sequence diagram shows configuration as function arguments. |

**Who I clarified with**: Andrew (21.07.2026)
- Andrew: *"Mhm, so how do you figure is the most efficient way to get a function with all those arguments"*
- Elroy: *"More of personal preference I like the UIUX more interactive, flags don't really make sense for me."*
- Andrew: *"Trust me, it's the normal way to do it"*

**Outcome**: Implemented `--generate-config` flag that outputs JSON to stdout. User pipes to config file. Daemon mode only reads config, never writes during runtime.

### Decision 3: Buffer Directory (`buffer_dir`)

| Uncertainty | How I Resolved It |
|-------------|-------------------|
| Why does the daemon need its own buffer directory? Why not use `/tmp/` or the system `pkg` cache? | I questioned this and received clarification: the buffer is for downloading packages from peers (UC-02) and serving them to other peers (UC-06). It needs to persist across reboots (unlike `/tmp`) and be user-writable (unlike `/var/cache/pkg`). |

So  there is 3 Config fields

#### 3. Config Fields (Exactly 3 per spec)
| Field | Default | Validation |
|-------|---------|------------|
| `tracker_url` | `http://127.0.0.1:8080` | Valid HTTP/HTTPS URL |
| `listen_addr` | `127.0.0.1:9001` | `host:port`, port 1024-65535 |
| `buffer_dir` | `$HOME/.cache/jmj` | Directory must be creatable/writable |

**Who I clarified with**: Andrew (read the spec carefully), then confirmed with the team

**Outcome**: Use `~/.cache/jmj` as default. This follows XDG standards, is user-writable, and persists across reboots.

### Decision 4: Permission Model & Config Writing

| Uncertainty | How I Resolved It |
|-------------|-------------------|
| The spec says the daemon writes to `/etc/jmj/config.json`, but users don't have root access. How should this work? | I initially suggested writing to `/tmp/` and having the user `sudo mv` it. Andrew's `wpa_supplicant` example showed a better pattern: generate to stdout, pipe with `sudo tee`. |

**Who I clarified with**: Andrew (via the `wpa_supplicant` analogy)
- Andrew loves pipes and uses `wpa_supplicant` as a reference for the flag-based approach.
- Pattern: `wpa_passphrase "SSID" "PASS" | sudo tee /etc/wpa_supplicant.conf`

**Outcome**: Daemon never writes to `/etc/` directly. Generator outputs JSON to stdout; admin uses `sudo tee` to write system-wide configs.

### Decision 5: Hot Reload Mechanism

| Uncertainty | How I Resolved It |
|-------------|-------------------|
| How should the daemon reload config without restarting? | The spec requires hot reload. I researched and chose SIGHUP (signal 1), which is the standard Unix pattern for daemon reload (used by nginx, systemd, etc.). |

**Who I clarified with**: Self/AI (researched standard Unix patterns)

**Outcome**: Implemented SIGHUP handler. `kill -SIGHUP $(pgrep jmj)` triggers config reload. The daemon validates the new config before applying it.

### Decision 6 Partial Updates

| Uncertainty | How I Resolved It |
|-------------|-------------------|
| When the user provides CLI flags AND a config file exists, which takes precedence? How do we handle partial updates?|	The spec requires "partial updates" – only specified fields are overridden. I implemented this by: 1) Loading the config file, 2) Overriding ONLY fields where CLI flags were explicitly provided (non-empty), 3) Leaving all other fields unchanged from the config file.

**Who I clarified with** confirmed with Andrew via the sequence diagram. 

**Outcome**: Partial updates work as spec requires. Flags override only the specified config fields. -id and -cids remain runtime-only and are never written to the config file.


## Implementation Details

### Files Modified/Created

#### `cmd/jmj/main.go`
- Added `--generate-config` flag (need to include `> ~/.config/jmj/config.json` won't automaticaly write to config file)
- Added config path resolution (defaults to `~/.config/jmj/config.json`)
- Simplified to delegate daemon logic to `internal/daemon`
- Required flags: `-id`, `-cids`

#### `internal/config/config.go` (NEW)
- `DaemonConfig` struct with JSON tags
- `DefaultConfig()` - hardcoded defaults
- `Load(path)` - reads JSON, handles missing/corrupt (`.bak`)
- `Validate(cfg)` - URL, port, directory checks
- `Save(path, cfg)` - writes JSON (for generator only)

#### `internal/daemon/daemon.go` (NEW)
- `Daemon` struct with config, client, HTTP server
- `Start()` - initializes HTTP server, discovery client, heartbeat
- `Reload()` - hot reload via SIGHUP
- `Stop()` - graceful shutdown
- `startHTTPServer()` - placeholder for UC-06

#### `internal/discovery/client.go` (MODIFIED)
- Added `stopChan` field
- Added `Stop()` method for graceful shutdown
- Fixed `RunHeartbeat()` to use `select` instead of `switch`

## Testing Commands

```bash
# 1. Generate config
mkdir -p ~/.config/jmj
./jmj --generate-config -tracker http://localhost:8080 -addr 127.0.0.1:9001 > ~/.config/jmj/config.json

# 2. Verify config file
cat ~/.config/jmj/config.json

# 3. Start tracker (in another terminal)
./trac

# 4. Start daemon
CID=$(printf 'a%.0s' {1..64})
./jmj -id alice -cids $CID

# 5. Test SIGHUP reload (in another terminal)
kill -SIGHUP $(pgrep jmj)

# 6. Test HTTP server (placeholder - UC-06) shouldn't work now
curl -X POST http://127.0.0.1:9001/ping

# 7. Graceful shutdown for daemon
kill -TERM $(pgrep jmj)
```

## Update 2026-07-31: Fail-fast validation fix

During a review of `cmd/jmj/main.go` against `docs/uc-01.puml`, a deviation was found:

**Problem**: The daemon mode called `config.Load()` *before* `config.Validate()`. `Load()` reads the config file and, on a corrupt file, renames it to `config.bak` — a disk write. This violated the diagram's `validateArgs()` note: *"Fail fast, before touching disk"* / *"Config file untouched"*. Verified with a test: a corrupt config plus an invalid `-addr` (port 80) produced `config.json.bak` even though the args were rejected.

**Fix**: Validate the requested settings *first*, before any config-file I/O:
1. Build a candidate config from `config.DefaultConfig()` + flag overrides.
2. `config.Validate(candidate)` — fail fast; invalid args exit before the file is read.
3. Only then `config.Load(path)` (missing → defaults, corrupt → `.bak` + defaults).
4. Re-merge flag overrides and `config.Validate` again to catch invalid values inside the file.

Refactored the duplicate override logic into `applyOverrides(cfg, tracker, addr, buffer)`.

**Verification**:
- Corrupt config + invalid args → error, `config.json` left untouched (no `.bak`). ✓
- Corrupt config + valid args → `.bak` created, defaults used. ✓
- Happy path: `-tracker` override merged, `listen_addr` kept from file, announce + `/peers` lookup work. ✓
- `go build`, `go vet`, `go test ./...` all green.

## Update 2026-07-31: Unit tests against the sequence diagram + Reload deadlock fix

### Deadlock fix (found while writing tests)

`Daemon.Reload()` in `internal/daemon/daemon.go` held `d.mu` and then called
`startHTTPServer()` / `startDiscovery()`, both of which lock `d.mu` again.
`sync.Mutex` is not reentrant, so any SIGHUP reload that changed `listen_addr`
or `tracker_url` **deadlocked**. The reload tests would have hung forever on the
old code.

**Fix**: `Reload()` now releases `d.mu` before calling the re-locking helpers.
The read/validate/merge still happens under the lock; the restarts happen after
it. (Uncertainty resolved: this is a clear bug, not a spec ambiguity — the UC-01
diagram requires hot reload to work.)

### New test files

**`internal/config/config_test.go`** (table-driven, no binary):
- `TestDefaultConfig` — the 3 defaults
- `TestLoadReadable` — R1 (readable file → currentSettings)
- `TestLoadMissing` — R2 (missing → defaults, **file stays absent**)
- `TestLoadCorrupt` — R3 (corrupt → `.bak` preserves original bytes, defaults)
- `TestValidate` — URL / port range (1024–65535) / buffer-dir writability, pos+neg
- `TestSaveAndLoadRoundTrip` — Save writes what Load reads

**`internal/daemon/daemon_test.go`** (constructs `Daemon` in-package; fake
tracker = `net/http/httptest`):
- `TestReloadListenAddrChange` — H1 (server restarts on new addr, old addr closes)
- `TestReloadTrackerChange` — H2 (re-announces to the new tracker)
- `TestReloadInvalidFile` — H3 (invalid file rejected, old settings stay)
- `TestReloadCorruptFile` — H4 (corrupt → `.bak`, defaults applied)
- `TestReloadDropsCLIOverrides` — H5 (see flagged caveat below)
- `TestStartAnnounces` — R8 (Start announces → "peer registered", health endpoint up)
- `TestStop` — servers and heartbeat close cleanly

### Flagged to spec owner (open question)

**H5 — SIGHUP drops startup CLI overrides.** `Reload()` reads only the config
file, so a `-tracker X` / `-addr Y` flag given at startup is silently reverted to
the file's value on the next SIGHUP. `TestReloadDropsCLIOverrides` pins the
current behaviour. Decision needed: is "file wins" intended (flags are
startup-only), or should Reload re-apply the startup flags?

### Not unit-testable without the binary (covered by the manual commands above)

- G1–G3: `--generate-config` stdout/exit codes — logic in `func main()`
- R4: fail-fast *ordering* (file untouched on invalid args) — ordering in `func main()`
- R7: `-id`/`-cids` required check — in `func main()`

We deliberately did **not** refactor the flow out of `main.go` (kept as-is), and
integration tests are out of scope for now, so these branches stay manual-verified.

## Full test-case matrix (mapped to the UC-01 sequence diagram)

Naming: **G** = Generate-config section, **R** = Run-daemon section, **H** =
Hot-reload section. Status column: `unit test` = covered by an automated unit
test, `manual` = verified by hand only (logic lives in `func main()`, no
refactor done).

### Generate config — `jmj --generate-config [-tracker] [-addr] [-buffer]`

| # | Sequence-diagram branch | Test case | Workflow it fits | Status |
|---|------------------------|-----------|------------------|--------|
| G1 | args valid → JSON on stdout | run with valid `-tracker`/`-addr`/`-buffer` → full JSON config printed to stdout, exit 0 | `jmj --generate-config \| sudo tee config.json` | manual |
| G2 | invalid setting → error, stdout empty | bad port (80), bad URL, or unwritable buffer dir → error on stderr, exit 1, nothing on stdout | user typos a flag while generating | manual |
| G3 | defaults (no flags) | run with no flags → all 3 defaults (`tracker_url`, `listen_addr`, `buffer_dir`) emitted | quick throwaway config | manual |

### Run daemon — `jmj -id <peer-id> -cids <list> [-config <path>]`

| # | Sequence-diagram branch | Test case | Workflow it fits | Status |
|---|------------------------|-----------|------------------|--------|
| R1 | readConfig → file readable | config file contains settings → those settings are used | run after generating a config | unit test `TestLoadReadable` |
| R2 | file missing → notFound | no config file → defaults used, and the daemon does **not** create the file | first run before any config exists | unit test `TestLoadMissing` |
| R3 | file corrupted → moveTo("config.bak") | garbage file → renamed to `.bak` (bytes preserved), defaults used | config file got mangled | unit test `TestLoadCorrupt` |
| R4 | invalid setting → config untouched | corrupt file + invalid `-addr` → error, and the file is **not** renamed (fail-fast happens before any disk I/O) | typo'd a flag while a bad config file happens to exist | manual (ordering lives in `func main`) |
| R5 | merge(currentSettings ?: defaults, changedKeys) | file has `listen_addr`, pass only `-tracker` → tracker overridden, listen_addr kept | override one field without rewriting the whole file | manual (merge logic in `func main`) |
| R6 | no write-back | after the daemon runs, the config file is byte-identical | confirms the daemon treats the file as read-only | manual |
| R7 | `-id`/`-cids` required | run with either missing → error before any file I/O | forgot the identity flags | manual (in `func main`) |
| R8 | announce → acknowledgement (peer registered) | `Start()` against a fake tracker → `/announce` received, health endpoint up | the "configured and ready" handoff | unit test `TestStartAnnounces` |

### Hot reload — `kill -SIGHUP <pid>`

| # | Sequence-diagram branch | Test case | Workflow it fits | Status |
|---|------------------------|-----------|------------------|--------|
| H1 | apply: listen_addr changed | edit file, `Reload()` → HTTP server restarts and answers on the new port; old port closes | edit config while running and apply it | unit test `TestReloadListenAddrChange` |
| H2 | apply: tracker_url changed | edit file, `Reload()` → re-announces to the **new** tracker | pointing the daemon at a different tracker | unit test `TestReloadTrackerChange` |
| H3 | invalid file → old settings stay | invalid config file, `Reload()` returns error, old settings still in effect | bad edit; no restart needed | unit test `TestReloadInvalidFile` |
| H4 | corrupt file on reload | corrupt config → `.bak` created, defaults applied | mangled file while running | unit test `TestReloadCorruptFile` |
| H5 | apply(settings) — file wins | start with `-tracker X` (file has Y), `Reload()` → config reverts to Y; startup CLI override is **not** re-applied | documents current behavior (see flagged caveat above) | unit test `TestReloadDropsCLIOverrides` |

### Gaps (not covered by any test)

- **G1–G3, R4, R7**: live in `func main()` (`cmd/jmj/main.go`) — need the binary
  or a refactor; integration tests are out of scope for now.
- **R5, R6**: merge and no-write-back are done inline in `func main()` too, so
  they have no unit test yet either.
- **SIGHUP signal delivery itself**: the reload *logic* is tested by calling
  `Reload()` directly; actually sending the `kill -SIGHUP` signal to a process
  is only covered by the manual commands in "Testing Commands".