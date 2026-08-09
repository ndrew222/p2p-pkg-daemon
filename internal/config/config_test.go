package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validConfig is a config that passes, so each test can break exactly one
// field and know that is what it is testing.
func validConfig(t *testing.T) *DaemonConfig {
	t.Helper()
	return &DaemonConfig{
		TrackerURL:  "http://127.0.0.1:8080",
		FacadeAddr:  "127.0.0.1:9001",
		ServingAddr: "0.0.0.0:9002",
		TempDir:     filepath.Join(t.TempDir(), "scratch"),
		CacheDir:    t.TempDir(),
		RepoDBDir:   t.TempDir(),
		UpstreamURL: "https://pkg.example.org/FreeBSD:15:amd64/quarterly",
	}
}

func TestValidateAcceptsAValidConfig(t *testing.T) {
	if err := Validate(validConfig(t)); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TempDir is daemon-owned, so Validate creates it. This is the contrast case
// for the CacheDir tests below.
func TestValidateCreatesTempDir(t *testing.T) {
	cfg := validConfig(t)
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if info, err := os.Stat(cfg.TempDir); err != nil || !info.IsDir() {
		t.Errorf("temp_dir was not created: %v", err)
	}
}

// The loopback rule on facade_addr is mandatory, not advisory: the facade
// drives this daemon's fetch loop on behalf of a local pkg, and off-host
// access would make it an open relay.
func TestValidateFieldsRejectsNonLoopbackFacadeAddr(t *testing.T) {
	rejected := []struct {
		name string
		addr string
	}{
		{"routable IPv4", "203.0.113.7:9001"},
		{"all interfaces IPv4", "0.0.0.0:9001"},
		{"empty host", ":9001"},
		{"all interfaces IPv6", "[::]:9001"},
		{"hostname that is not localhost", "example.com:9001"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.FacadeAddr = tc.addr
			if err := ValidateFields(cfg); err == nil {
				t.Errorf("ValidateFields() with facade_addr %q = nil, want an error", tc.addr)
			}
		})
	}
}

func TestValidateFieldsAcceptsLoopbackFacadeAddr(t *testing.T) {
	accepted := []string{
		"127.0.0.1:9001",
		"127.0.0.53:9001",
		"[::1]:9001",
		"localhost:9001",
	}
	for _, addr := range accepted {
		t.Run(addr, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.FacadeAddr = addr
			if err := ValidateFields(cfg); err != nil {
				t.Errorf("ValidateFields() with facade_addr %q = %v, want nil", addr, err)
			}
		})
	}
}

// serving_addr is the opposite case: peers are on other machines by
// definition, so binding every interface is the normal configuration.
func TestValidateFieldsAcceptsPublicServingAddr(t *testing.T) {
	cfg := validConfig(t)
	cfg.ServingAddr = "0.0.0.0:9002"
	if err := ValidateFields(cfg); err != nil {
		t.Errorf("ValidateFields() with a public serving_addr = %v, want nil", err)
	}
}

func TestValidateFieldsRejectsBadPorts(t *testing.T) {
	t.Run("facade_addr privileged port", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.FacadeAddr = "127.0.0.1:80"
		if err := ValidateFields(cfg); err == nil {
			t.Error("port 80 = nil, want an error")
		}
	})
	t.Run("serving_addr out of range", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.ServingAddr = "0.0.0.0:70000"
		if err := ValidateFields(cfg); err == nil {
			t.Error("port 70000 = nil, want an error")
		}
	})
	t.Run("serving_addr missing port", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.ServingAddr = "0.0.0.0"
		if err := ValidateFields(cfg); err == nil {
			t.Error("missing port = nil, want an error")
		}
	})
}

// ServingPort is what reaches the tracker as servingPort. It comes off
// serving_addr and nowhere else.
func TestServingPort(t *testing.T) {
	cfg := validConfig(t)
	cfg.ServingAddr = "0.0.0.0:4711"
	port, err := cfg.ServingPort()
	if err != nil {
		t.Fatalf("ServingPort: %v", err)
	}
	if port != 4711 {
		t.Errorf("ServingPort() = %d, want 4711", port)
	}

	// And it is independent of the facade's port.
	cfg.FacadeAddr = "127.0.0.1:9001"
	if port, _ := cfg.ServingPort(); port != 4711 {
		t.Errorf("ServingPort() = %d after changing facade_addr, want 4711", port)
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

// The repository databases are pkg's signed catalogues. The same read-only rule
// as the cache applies, and harder: creating this directory would produce a
// daemon that verifies nothing and 404s every package request.
func TestValidateDoesNotCreateRepoDBDir(t *testing.T) {
	cfg := validConfig(t)
	cfg.RepoDBDir = filepath.Join(t.TempDir(), "no-such-repos")

	if err := Validate(cfg); err == nil {
		t.Fatal("Validate() with a missing repo_db_dir = nil, want an error")
	}
	if _, err := os.Stat(cfg.RepoDBDir); !os.IsNotExist(err) {
		t.Errorf("Validate() created %q; the repository database is read-only", cfg.RepoDBDir)
	}
}

func TestValidateRejectsRepoDBDirThatIsNotADirectory(t *testing.T) {
	cfg := validConfig(t)
	cfg.RepoDBDir = filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(cfg.RepoDBDir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Validate(cfg); err == nil {
		t.Error("Validate() with a file as repo_db_dir = nil, want an error")
	}
}

// -generate-config writes a config for whatever host will run it. The default
// cache_dir is a FreeBSD path, so requiring it to exist here would break the
// generator on every machine anyone develops on.
func TestValidateFieldsIgnoresTheFilesystem(t *testing.T) {
	cfg := defaultsWithUpstream()
	cfg.TempDir = filepath.Join(t.TempDir(), "not-created-either")

	if err := ValidateFields(cfg); err != nil {
		t.Fatalf("ValidateFields() on the defaults = %v, want nil", err)
	}
	// And no side effects: it must not have created anything.
	if _, err := os.Stat(cfg.TempDir); !os.IsNotExist(err) {
		t.Errorf("ValidateFields() created %q", cfg.TempDir)
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
	t.Run("temp_dir", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.TempDir = ""
		if err := ValidateFields(cfg); err == nil {
			t.Error("empty temp_dir = nil, want an error")
		}
	})
	t.Run("repo_db_dir", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.RepoDBDir = ""
		if err := ValidateFields(cfg); err == nil {
			t.Error("empty repo_db_dir = nil, want an error")
		}
	})
}

// The default temp_dir is the OS scratch directory, not a directory of the
// daemon's own. The buffer is per-request and ephemeral; nothing about a
// download needs to survive a reboot, so there is no ~/.cache/jmj.
func TestDefaultConfigTempDir(t *testing.T) {
	if got := DefaultConfig().TempDir; got != os.TempDir() {
		t.Errorf("default temp_dir = %q, want %q", got, os.TempDir())
	}
}

// defaultsWithUpstream is the defaults plus the one key that has none.
//
// upstream_url is required and deliberately undefaulted (ADR-006), so
// DefaultConfig() alone no longer validates -- see
// TestDefaultConfigIsRefusedWithoutAnUpstream, which asserts exactly that.
// Every other test wants "a valid config", which is this.
func defaultsWithUpstream() *DaemonConfig {
	cfg := DefaultConfig()
	cfg.UpstreamURL = "https://pkg.example.org/FreeBSD:15:amd64/quarterly"
	return cfg
}

// The default facade address must itself satisfy the loopback rule, or the
// daemon refuses to start out of the box.
//
// Rephrased for ADR-006: "the defaults are valid by construction" now means
// the defaults plus the one required key, because the generator refuses to
// emit a config without it rather than guessing a value.
func TestDefaultConfigPassesFieldValidation(t *testing.T) {
	if err := ValidateFields(defaultsWithUpstream()); err != nil {
		t.Errorf("ValidateFields(defaults + upstream) = %v, want nil", err)
	}
}

// The other half of the invariant above, and the reason -generate-config
// aborts rather than emitting a config that cannot start (ADR-006). The error
// must name the key: a config silently defaulted to some mirror would choose a
// repository on the operator's behalf, and a wrong branch does not error --
// pkg populates its database from whatever is proxied and every hash matches.
func TestDefaultConfigIsRefusedWithoutAnUpstream(t *testing.T) {
	err := ValidateFields(DefaultConfig())
	if err == nil {
		t.Fatal("ValidateFields(DefaultConfig()) = nil, want an error naming upstream_url")
	}
	if !strings.Contains(err.Error(), "upstream_url") {
		t.Errorf("error %q does not name upstream_url", err)
	}
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
	old := `{"tracker_url":"http://10.0.0.1:8080","facade_addr":"127.0.0.1:9005"}`
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
	if cfg.FacadeAddr != "127.0.0.1:9005" {
		t.Errorf("facade_addr = %q, want the value from the file", cfg.FacadeAddr)
	}
	// The keys it lacks take the defaults.
	if cfg.CacheDir != "/var/cache/pkg" {
		t.Errorf("cache_dir = %q, want the default", cfg.CacheDir)
	}
	if cfg.ServingAddr != DefaultConfig().ServingAddr {
		t.Errorf("serving_addr = %q, want the default", cfg.ServingAddr)
	}
	// The file must still be there -- an old-but-parsable config is not
	// corrupt, so Load must not have moved it aside.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Load() moved a parsable config aside: %v", err)
	}
}

// A v0.1 config is valid JSON, so encoding/json would silently drop its
// listen_addr and buffer_dir and start the daemon on defaults -- on the wrong
// ports, with the user's settings ignored. Say so instead.
func TestLoadRejectsLegacyKeys(t *testing.T) {
	for _, key := range []string{"listen_addr", "buffer_dir"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			body := `{"tracker_url":"http://10.0.0.1:8080","` + key + `":"x"}`
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load() with %q = nil, want an error", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error %q does not name the offending key %q", err, key)
			}
			// A legacy config is wrong, not corrupt: it holds settings
			// the user still wants, so it must be left where it is.
			if _, err := os.Stat(path); err != nil {
				t.Errorf("Load() moved a legacy config aside: %v", err)
			}
			if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
				t.Error("Load() created a .bak for a legacy config")
			}
		})
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

// --- upstream_url (ADR-006) ---------------------------------------------

var errNoABI = errors.New("no pkg on this host")

func TestValidateFieldsUpstreamURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		// naming is a substring the error must carry, so a rejection is
		// actionable rather than merely correct.
		naming string
	}{
		{name: "https", url: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly"},
		{name: "plaintext http is allowed, not refused", url: "http://mirror.lan/x"},
		{
			name: "the ${ABI} placeholder survives field validation",
			url:  "https://pkg.FreeBSD.org/${ABI}/quarterly",
		},
		{name: "empty", url: "", wantErr: true, naming: "upstream_url"},
		{
			// The obvious paste from /etc/pkg/FreeBSD.conf. Named, not
			// silently stripped -- same principle as legacyKeys.
			name:    "pkg+https is rejected with the fix in the message",
			url:     "pkg+https://pkg.FreeBSD.org/${ABI}/quarterly",
			wantErr: true,
			naming:  "pkg+",
		},
		{name: "ftp", url: "ftp://mirror.example/x", wantErr: true, naming: "http"},
		{name: "no scheme", url: "pkg.FreeBSD.org/quarterly", wantErr: true},
		{name: "no host", url: "https:///quarterly", wantErr: true, naming: "host"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.UpstreamURL = tc.url
			err := ValidateFields(cfg)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateFields() with upstream_url %q = nil, want an error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateFields() with upstream_url %q = %v, want nil", tc.url, err)
			}
			if tc.naming != "" && !strings.Contains(err.Error(), tc.naming) {
				t.Errorf("error %q does not mention %q", err, tc.naming)
			}
		})
	}
}

// Plaintext is permitted but must not pass silently. The asymmetry with
// facade_addr -- which IS refused -- is deliberate: a non-loopback facade is an
// open relay, whereas tampering with a plaintext upstream is still caught by
// pkg's signature check and jmj's hash check.
func TestWarningsFlagsPlaintextUpstream(t *testing.T) {
	cfg := validConfig(t)
	cfg.UpstreamURL = "http://mirror.lan/x"
	got := Warnings(cfg)
	if len(got) != 1 || !strings.Contains(got[0], "https") {
		t.Errorf("Warnings() = %v, want one warning suggesting https", got)
	}

	cfg.UpstreamURL = "https://mirror.lan/x"
	if got := Warnings(cfg); len(got) != 0 {
		t.Errorf("Warnings() on an https upstream = %v, want none", got)
	}
}

func TestExpandUpstream(t *testing.T) {
	// called records whether the ABI lookup ran at all. Whether it runs is
	// as much a part of the contract as the result: a literal URL must never
	// shell out, or the daemon stops working on a non-FreeBSD box.
	newABI := func(v string, err error, called *bool) ABIFunc {
		return func() (string, error) { *called = true; return v, err }
	}

	t.Run("expands the placeholder", func(t *testing.T) {
		var called bool
		cfg := &DaemonConfig{UpstreamURL: "https://pkg.FreeBSD.org/${ABI}/quarterly"}
		if err := ExpandUpstream(cfg, newABI("FreeBSD:15:amd64", nil, &called)); err != nil {
			t.Fatalf("ExpandUpstream() = %v, want nil", err)
		}
		want := "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly"
		if cfg.UpstreamURL != want {
			t.Errorf("upstream_url = %q, want %q", cfg.UpstreamURL, want)
		}
		if !called {
			t.Error("the ABI lookup was not called for a URL carrying ${ABI}")
		}
	})

	t.Run("a literal URL never runs pkg", func(t *testing.T) {
		var called bool
		cfg := &DaemonConfig{UpstreamURL: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly"}
		if err := ExpandUpstream(cfg, newABI("", errNoABI, &called)); err != nil {
			t.Fatalf("ExpandUpstream() = %v, want nil", err)
		}
		if called {
			t.Error("the ABI lookup ran for a URL with no placeholder; a literal URL must not shell out")
		}
	})

	t.Run("an unresolvable ABI is fatal", func(t *testing.T) {
		var called bool
		cfg := &DaemonConfig{UpstreamURL: "https://pkg.FreeBSD.org/${ABI}/quarterly"}
		err := ExpandUpstream(cfg, newABI("", errNoABI, &called))
		if err == nil {
			t.Fatal("ExpandUpstream() = nil, want an error; proxying with an unexpanded placeholder is the bad outcome")
		}
		if !strings.Contains(err.Error(), "upstream_url") {
			t.Errorf("error %q does not name the field", err)
		}
	})

	t.Run("a variable we do not expand is refused", func(t *testing.T) {
		var called bool
		cfg := &DaemonConfig{UpstreamURL: "https://pkg.FreeBSD.org/${OSNAME}/quarterly"}
		if err := ExpandUpstream(cfg, newABI("FreeBSD:15:amd64", nil, &called)); err == nil {
			t.Error("ExpandUpstream() with an unknown placeholder = nil, want an error rather than a URL with ${...} in the path")
		}
	})
}

// --- ADR-002 seeding caps ----------------------------------------------------

// The two key names are an owner ruling (HANDOFF §4.7); ADR-002 left them
// unnamed. Pinning the JSON spelling here is what stops a later rename passing
// review: a renamed key does not fail loudly, it silently reverts an operator's
// cap to unlimited, which is the one outcome the cap exists to prevent.
func TestSeedingCapKeyNamesAndDefaults(t *testing.T) {
	if got := DefaultConfig().MaxConcurrentSeeds; got != 0 {
		t.Errorf("default max_concurrent_seeds = %d, want 0 (unlimited)", got)
	}
	if got := DefaultConfig().MaxConcurrentSeedsPerIP; got != 0 {
		t.Errorf("default max_concurrent_seeds_per_ip = %d, want 0 (unlimited)", got)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"max_concurrent_seeds":8,"max_concurrent_seeds_per_ip":2}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.MaxConcurrentSeeds != 8 {
		t.Errorf("max_concurrent_seeds = %d, want 8", cfg.MaxConcurrentSeeds)
	}
	if cfg.MaxConcurrentSeedsPerIP != 2 {
		t.Errorf("max_concurrent_seeds_per_ip = %d, want 2", cfg.MaxConcurrentSeedsPerIP)
	}

	// The generator's output must carry both keys under exactly those
	// names, or a config round trip drops the operator's setting.
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"max_concurrent_seeds"`, `"max_concurrent_seeds_per_ip"`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("generated config does not carry %s: %s", key, out)
		}
	}
}

// 0 already means unlimited, so a negative value is a mistake -- most likely
// arithmetic that produced one -- and is reported rather than absorbed.
func TestValidateFieldsRejectsNegativeSeedingCaps(t *testing.T) {
	tests := []struct {
		name string
		set  func(*DaemonConfig)
		key  string
	}{
		{"global", func(c *DaemonConfig) { c.MaxConcurrentSeeds = -1 }, "max_concurrent_seeds"},
		{"per ip", func(c *DaemonConfig) { c.MaxConcurrentSeedsPerIP = -1 }, "max_concurrent_seeds_per_ip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.set(cfg)
			err := ValidateFields(cfg)
			if err == nil {
				t.Fatalf("ValidateFields() with a negative %s = nil, want an error", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name %s", err, tc.key)
			}
		})
	}
}

func TestValidateFieldsAcceptsSeedingCaps(t *testing.T) {
	for _, tc := range []struct{ global, perIP int }{
		{0, 0}, {8, 2}, {1, 1}, {0, 4}, {64, 0},
	} {
		cfg := validConfig(t)
		cfg.MaxConcurrentSeeds = tc.global
		cfg.MaxConcurrentSeedsPerIP = tc.perIP
		if err := ValidateFields(cfg); err != nil {
			t.Errorf("ValidateFields() with caps %d/%d = %v, want nil", tc.global, tc.perIP, err)
		}
	}
}

// ADR-002 requires diagnostics good enough to tell an attack from a
// misconfigured ceiling. A per-IP cap above the global one can never fire, so
// it is dead configuration and almost certainly transposed.
func TestWarningsFlagsAPerIPCapThatCannotFire(t *testing.T) {
	cfg := validConfig(t)
	cfg.MaxConcurrentSeeds = 2
	cfg.MaxConcurrentSeedsPerIP = 8
	got := Warnings(cfg)
	if len(got) != 1 || !strings.Contains(got[0], "max_concurrent_seeds_per_ip") {
		t.Errorf("Warnings() = %v, want one warning naming max_concurrent_seeds_per_ip", got)
	}

	// Unlimited globally is not a transposition: every per-IP value is
	// meaningful under it.
	cfg.MaxConcurrentSeeds = 0
	if got := Warnings(cfg); len(got) != 0 {
		t.Errorf("Warnings() with an unlimited global cap = %v, want none", got)
	}
}
