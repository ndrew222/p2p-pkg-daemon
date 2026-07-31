package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validCfg returns a config that passes Validate, using t.TempDir for the buffer dir.
func validCfg(t *testing.T) *DaemonConfig {
	t.Helper()
	return &DaemonConfig{
		TrackerURL: "http://127.0.0.1:8080",
		ListenAddr: "127.0.0.1:9001",
		BufferDir:  t.TempDir(),
	}
}

// TestDefaultConfig covers the R2/generator "defaults" branch: all three fields
// must have their hardcoded defaults.
func TestDefaultConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cfg := DefaultConfig()
	if cfg.TrackerURL != "http://127.0.0.1:8080" {
		t.Errorf("TrackerURL = %q, want %q", cfg.TrackerURL, "http://127.0.0.1:8080")
	}
	if cfg.ListenAddr != "127.0.0.1:9001" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, "127.0.0.1:9001")
	}
	wantBuffer := filepath.Join(home, ".cache", "jmj")
	if cfg.BufferDir != wantBuffer {
		t.Errorf("BufferDir = %q, want %q", cfg.BufferDir, wantBuffer)
	}
}

// TestLoadReadable covers the R1 branch: a readable file yields currentSettings.
func TestLoadReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := &DaemonConfig{
		TrackerURL: "http://tracker.example:8080",
		ListenAddr: "127.0.0.1:9301",
		BufferDir:  t.TempDir(),
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *want {
		t.Errorf("Load = %+v, want %+v", *got, *want)
	}
}

// TestLoadMissing covers the R2 branch: a missing file is not an error, returns
// defaults, and the daemon must NOT create the file (no write-back).
func TestLoadMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *DefaultConfig() {
		t.Errorf("Load = %+v, want defaults %+v", *got, *DefaultConfig())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Load created the config file; expected it to stay absent (stat err = %v)", err)
	}
}

// TestLoadCorrupt covers the R3 branch: a corrupt file is moved to .bak
// (preserving its bytes) and defaults are returned.
func TestLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	garbage := "this is not valid json"
	if err := os.WriteFile(path, []byte(garbage), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *DefaultConfig() {
		t.Errorf("Load = %+v, want defaults %+v", *got, *DefaultConfig())
	}

	bak := path + ".bak"
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("expected %s to exist after corrupt load", bak)
	}
	data, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", bak, err)
	}
	if string(data) != garbage {
		t.Errorf("bak content = %q, want original %q", string(data), garbage)
	}
}

// TestValidate covers the validateArgs branches (fail fast): tracker URL, port
// range, and directory writability.
func TestValidate(t *testing.T) {
	// blocker is a regular file; using it as a directory parent must fail MkdirAll.
	blockerDir := t.TempDir()
	blocker := filepath.Join(blockerDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*DaemonConfig)
		wantErr string
	}{
		{name: "valid", wantErr: ""},
		{name: "empty tracker url", mutate: func(c *DaemonConfig) { c.TrackerURL = "" }, wantErr: "tracker_url"},
		{name: "malformed tracker url", mutate: func(c *DaemonConfig) { c.TrackerURL = "http://[bad" }, wantErr: "tracker_url"},
		{name: "addr missing port", mutate: func(c *DaemonConfig) { c.ListenAddr = "127.0.0.1" }, wantErr: "listen_addr"},
		{name: "port below 1024", mutate: func(c *DaemonConfig) { c.ListenAddr = "127.0.0.1:80" }, wantErr: "1024-65535"},
		{name: "port above 65535", mutate: func(c *DaemonConfig) { c.ListenAddr = "127.0.0.1:65536" }, wantErr: "1024-65535"},
		{name: "port not a number", mutate: func(c *DaemonConfig) { c.ListenAddr = "127.0.0.1:abc" }, wantErr: "1024-65535"},
		{name: "buffer dir under a file", mutate: func(c *DaemonConfig) { c.BufferDir = filepath.Join(blocker, "sub") }, wantErr: "buffer_dir"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validCfg(t)
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			err := Validate(cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestSaveAndLoadRoundTrip verifies Save writes a file that Load can read back.
func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := &DaemonConfig{
		TrackerURL: "http://tracker.example:8080",
		ListenAddr: "127.0.0.1:9301",
		BufferDir:  t.TempDir(),
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *want {
		t.Errorf("round trip = %+v, want %+v", *got, *want)
	}
}
