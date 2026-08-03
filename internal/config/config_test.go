package config

import (
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

// A config written by an older build has no cache_dir. It must load rather
// than fail, so the daemon can report a useful validation error instead of
// treating the whole file as corrupt and silently renaming it to .bak.
func TestLoadConfigWithoutCacheDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"tracker_url":"http://127.0.0.1:8080","listen_addr":"127.0.0.1:9001","buffer_dir":"/tmp/jmj"}`
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TrackerURL != "http://127.0.0.1:8080" {
		t.Errorf("tracker_url = %q, want the value from the file", cfg.TrackerURL)
	}
	if cfg.CacheDir != "" {
		t.Errorf("cache_dir = %q, want empty for a pre-cache_dir config", cfg.CacheDir)
	}
	// And an empty cache_dir must not pass validation.
	if err := Validate(cfg); err == nil {
		t.Error("Validate() with an empty cache_dir = nil, want an error")
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

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := validConfig(t)

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CacheDir != want.CacheDir {
		t.Errorf("cache_dir = %q, want %q", got.CacheDir, want.CacheDir)
	}
	if got.BufferDir != want.BufferDir || got.ListenAddr != want.ListenAddr {
		t.Errorf("round trip lost a field: %+v", got)
	}
}
