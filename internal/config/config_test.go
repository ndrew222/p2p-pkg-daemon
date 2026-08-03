package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// validConfig is a config that passes, so each test can break exactly one
// field and know that is what it is testing.
func validConfig(t *testing.T) *DaemonConfig {
	t.Helper()
	return &DaemonConfig{
		TrackerURL: "http://127.0.0.1:8080",
		ListenAddr: "127.0.0.1:9001",
		BufferDir:  filepath.Join(t.TempDir(), "buffer"),
		CacheDir:   t.TempDir(),
	}
}

func TestValidateAcceptsAValidConfig(t *testing.T) {
	if err := Validate(validConfig(t)); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// BufferDir is daemon-owned, so Validate creates it. This is the contrast case
// for the CacheDir tests below.
func TestValidateCreatesBufferDir(t *testing.T) {
	cfg := validConfig(t)
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if info, err := os.Stat(cfg.BufferDir); err != nil || !info.IsDir() {
		t.Errorf("buffer_dir was not created: %v", err)
	}
}

// The pkg cache is read-only to this daemon (AGENTS.md hard constraint), so a
// missing cache_dir is a configuration error, never a directory to create.
// Creating it would also turn a typo into a daemon that watches an empty
// directory forever and announces nothing.
func TestValidateDoesNotCreateCacheDir(t *testing.T) {
	cfg := validConfig(t)
	cfg.CacheDir = filepath.Join(t.TempDir(), "no-such-cache")

	if err := Validate(cfg); err == nil {
		t.Fatal("Validate() with a missing cache_dir = nil, want an error")
	}
	if _, err := os.Stat(cfg.CacheDir); !os.IsNotExist(err) {
		t.Errorf("Validate() created %q; the pkg cache is read-only", cfg.CacheDir)
	}
}

func TestValidateRejectsCacheDirThatIsNotADirectory(t *testing.T) {
	cfg := validConfig(t)
	cfg.CacheDir = filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(cfg.CacheDir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Validate(cfg); err == nil {
		t.Error("Validate() with a file as cache_dir = nil, want an error")
	}
}

// -generate-config writes a config for whatever host will run it. The default
// cache_dir is a FreeBSD path, so requiring it to exist here would break the
// generator on every machine anyone develops on.
func TestValidateFieldsIgnoresTheFilesystem(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BufferDir = filepath.Join(t.TempDir(), "not-created-either")

	if err := ValidateFields(cfg); err != nil {
		t.Fatalf("ValidateFields() on the defaults = %v, want nil", err)
	}
	// And no side effects: it must not have created anything.
	if _, err := os.Stat(cfg.BufferDir); !os.IsNotExist(err) {
		t.Errorf("ValidateFields() created %q", cfg.BufferDir)
	}
}

func TestValidateFieldsRequiresPathsToBeSet(t *testing.T) {
	t.Run("cache_dir", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.CacheDir = ""
		if err := ValidateFields(cfg); err == nil {
			t.Error("empty cache_dir = nil, want an error")
		}
	})
	t.Run("buffer_dir", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.BufferDir = ""
		if err := ValidateFields(cfg); err == nil {
			t.Error("empty buffer_dir = nil, want an error")
		}
	})
}

func TestDefaultConfigCacheDir(t *testing.T) {
	// The path named in UC-05, UC-06 and the use-case table.
	if got := DefaultConfig().CacheDir; got != "/var/cache/pkg" {
		t.Errorf("default cache_dir = %q, want /var/cache/pkg", got)
	}
}

// A config written by an older build has no cache_dir key at all. It must keep
// working: absent keys take the default rather than the zero value, so the
// user is not handed an empty path and a validation failure for a file they
// never edited.
func TestConfigWithoutCacheDirTakesTheDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"tracker_url":"http://10.0.0.1:8080","listen_addr":"127.0.0.1:9002","buffer_dir":"/tmp/jmj"}`
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Keys the file does have still win over the defaults.
	if cfg.TrackerURL != "http://10.0.0.1:8080" {
		t.Errorf("tracker_url = %q, want the value from the file", cfg.TrackerURL)
	}
	if cfg.ListenAddr != "127.0.0.1:9002" {
		t.Errorf("listen_addr = %q, want the value from the file", cfg.ListenAddr)
	}
	// The key it lacks takes the default.
	if cfg.CacheDir != "/var/cache/pkg" {
		t.Errorf("cache_dir = %q, want the default", cfg.CacheDir)
	}
	// The file must still be there -- an old-but-parsable config is not
	// corrupt, so Load must not have moved it aside.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Load() moved a parsable config aside: %v", err)
	}
}

func TestLoadMissingConfigReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CacheDir != "/var/cache/pkg" {
		t.Errorf("cache_dir = %q, want the default", cfg.CacheDir)
	}
}

// What -generate-config prints must be what the loader reads back. The generator
// emits JSON to stdout and the user redirects it, so the encoder and the
// parser are the round trip -- there is no Save in between, deliberately.
func TestGeneratedConfigRoundTrips(t *testing.T) {
	want := validConfig(t)

	generated, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, generated, 0644); err != nil {
		t.Fatal(err)
	}

	got, _, err := read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if *got != *want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// read must not touch the disk, including for a corrupt file. -generate-config
// depends on this: generating a config must never modify one.
func TestReadHasNoSideEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const garbage = `{"tracker_url": NOT JSON`
	if err := os.WriteFile(path, []byte(garbage), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, corrupt, err := read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !corrupt {
		t.Error("read did not report a corrupt file")
	}
	if cfg.TrackerURL != DefaultConfig().TrackerURL {
		t.Errorf("corrupt file did not fall back to defaults: %+v", cfg)
	}

	// The file is exactly as it was, and no .bak appeared beside it.
	back, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read moved or deleted the config: %v", err)
	}
	if string(back) != garbage {
		t.Errorf("read rewrote the config: %q", back)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("read created a .bak; that repair belongs to Load, not read")
	}
}

// Load is the daemon-startup path and does perform that one repair.
func TestLoadMovesCorruptConfigAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`not json`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TrackerURL != DefaultConfig().TrackerURL {
		t.Errorf("corrupt config did not fall back to defaults: %+v", cfg)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("corrupt config was not preserved as .bak: %v", err)
	}
}
