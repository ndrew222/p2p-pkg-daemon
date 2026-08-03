//This is the configuration library. It handles loading, validating, and saving configs.

package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// DaemonConfig holds persistent configuration fields. as JSON tag
type DaemonConfig struct {
	TrackerURL string `json:"tracker_url"`
	ListenAddr string `json:"listen_addr"`
	BufferDir  string `json:"buffer_dir"`

	// CacheDir is pkg's package cache, which the daemon watches to learn
	// what it can serve. READ-ONLY: the daemon writes only to BufferDir
	// and its config path. Unlike BufferDir, this one is never created --
	// see Validate.
	CacheDir string `json:"cache_dir"`
}

// DefaultConfig returns a config with hardcoded defaults.
// Need to change later just a proof of concept.
func DefaultConfig() *DaemonConfig {
	home, _ := os.UserHomeDir()
	return &DaemonConfig{
		TrackerURL: "http://127.0.0.1:8080",
		ListenAddr: "127.0.0.1:9001",
		BufferDir:  filepath.Join(home, ".cache", "jmj"),
		// FreeBSD's pkgng cache, as named in UC-05, UC-06 and the
		// use-case table. Overridable so the daemon can be exercised
		// on a non-FreeBSD box.
		CacheDir: "/var/cache/pkg",
	}
}

// Load reads JSON config from path. If file is missing, returns defaults.
// If file is corrupt, moves it to .bak and returns defaults (non-fatal).
func Load(path string) (*DaemonConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing config is not an error – use defaults.
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg DaemonConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Corrupt config: move to .bak and treat as missing.
		bakPath := path + ".bak"
		_ = os.Rename(path, bakPath) // ignore rename error; we still return defaults
		return DefaultConfig(), nil
	}

	return &cfg, nil
}

// ValidateFields checks everything that can be judged from the values alone.
// It touches the filesystem not at all and has no side effects, so it is safe
// to run against a config meant for a different machine -- which is exactly
// what -generate-config does. Validation happens BEFORE any file I/O (per spec).
func ValidateFields(cfg *DaemonConfig) error {
	// Tracker URL: must be a valid HTTP/HTTPS URL
	if _, err := url.ParseRequestURI(cfg.TrackerURL); err != nil {
		return fmt.Errorf("invalid tracker_url: %w", err)
	}

	// ListenAddr: must be "host:port" and port in 1024-65535
	_, portStr, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid listen_addr format (need host:port): %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1024 || port > 65535 {
		return fmt.Errorf("listen_addr port must be 1024-65535, got %q", portStr)
	}
	// Optional: host can be empty or IP; we don't need further validation.

	if cfg.BufferDir == "" {
		return fmt.Errorf("buffer_dir must be set")
	}
	if cfg.CacheDir == "" {
		return fmt.Errorf("cache_dir must be set (pkg's package cache, e.g. /var/cache/pkg)")
	}

	return nil
}

// Validate is the full startup check: ValidateFields plus the filesystem
// checks the daemon needs to be true on THIS machine before it runs.
//
// Do not call this from the config generator: a config being written for a
// FreeBSD host is perfectly valid on the Linux box writing it, even though
// /var/cache/pkg does not exist there.
func Validate(cfg *DaemonConfig) error {
	if err := ValidateFields(cfg); err != nil {
		return err
	}

	// BufferDir: must be creatable/writable.
	if err := os.MkdirAll(cfg.BufferDir, 0755); err != nil {
		return fmt.Errorf("buffer_dir not creatable/writable: %w", err)
	}
	// Also test write access by creating a temp file? MkdirAll ensures directory exists,
	// but permissions might be restrictive. We can attempt to create a temporary file.
	tmp, err := os.CreateTemp(cfg.BufferDir, "jmj-write-test-*")
	if err != nil {
		return fmt.Errorf("buffer_dir not writable: %w", err)
	}
	os.Remove(tmp.Name()) // clean up
	tmp.Close()

	// CacheDir: must already exist, and must be a directory.
	//
	// Deliberately NOT MkdirAll. The pkg cache is read-only to this daemon
	// (AGENTS.md: "the daemon writes only to its own temp buffer directory
	// and config path"), and creating it would also paper over the far more
	// likely case of a misconfigured path -- an empty directory the daemon
	// happily watches forever while announcing nothing.
	info, err := os.Stat(cfg.CacheDir)
	if err != nil {
		return fmt.Errorf("cache_dir is not readable (it must already exist; the daemon never creates it): %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cache_dir %q is not a directory", cfg.CacheDir)
	}

	return nil
}

// Save and write the Json condig to the file For generator only
func Save(path string, cfg *DaemonConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
