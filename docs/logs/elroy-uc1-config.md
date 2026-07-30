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