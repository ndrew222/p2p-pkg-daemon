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
}

// DefaultConfig returns a config with hardcoded defaults.
// Need to change later just a proof of concept.
func DefaultConfig() *DaemonConfig {
	home, _ := os.UserHomeDir()
	return &DaemonConfig{
		TrackerURL: "http://127.0.0.1:8080",
		ListenAddr: "127.0.0.1:9001",
		BufferDir:  filepath.Join(home, ".cache", "jmj"),
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

// Validate checks all fields and returns an error if any is invalid.
// Validation happens BEFORE any file I/O (per spec).
// It checks URL, port, directory checks
func Validate(cfg *DaemonConfig) error {
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
