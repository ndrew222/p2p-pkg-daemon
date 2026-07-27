# UC-1 Configuration System - Work Log

**Author**: Elroy Tan
**Date**: 2026-07-27
**Branch**: feature/uc1-config
**Status**: Complete

## Overview
Implemented the configuration system for the `jmj` daemon (UC-1). The system allows users to configure the daemon via CLI flags, with persistent storage in a JSON config file.

## Design Decisions

### 1. Config Path
- **Default**: `$HOME/.config/jmj/config.json` (XDG standard, user-writable)
- **Override**: `-config /path/to/file`
- **Rationale**: Avoids requiring `sudo` for normal operation. System-wide config can be used with `-config /etc/jmj/config.json` if pre-created.

### 2. Generator + Pipes Pattern
- **Approach**: `--generate-config` flag outputs JSON to stdout
- **Usage**: `./jmj --generate-config > ~/.config/jmj/config.json`
- **Rationale**: Follows Unix philosophy (inspired by `wpa_supplicant`). Separates config generation from daemon execution. Composable with tools like `jq` and `tee`.

### 3. Config Fields (Exactly 3 per spec)
| Field | Default | Validation |
|-------|---------|------------|
| `tracker_url` | `http://127.0.0.1:8080` | Valid HTTP/HTTPS URL |
| `listen_addr` | `127.0.0.1:9001` | `host:port`, port 1024-65535 |
| `buffer_dir` | `$HOME/.cache/jmj` | Directory must be creatable/writable |

### 4. Partial Updates
- CLI flags override config values when provided
- Only specified fields are overridden
- `-id` and `-cids` are NOT persisted (runtime-only)

### 5. Hot Reload (SIGHUP)
- Send `kill -SIGHUP $(pgrep jmj)` to reload config without restarting
- Validates new config before applying
- Restarts HTTP server if listen address changes
- Restarts discovery if tracker URL changes

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