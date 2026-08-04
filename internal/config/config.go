//This is the configuration library. It handles loading and validating configs.

package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
)

// DaemonConfig holds persistent configuration fields. as JSON tag
type DaemonConfig struct {
	TrackerURL string `json:"tracker_url"`

	// FacadeAddr is where pkg reaches the daemon: the mirror facade's
	// listener (UC-02, UC-07). It MUST be a loopback address -- see
	// validateFacadeAddr. pkg is the only client that has any business
	// talking to this port, and it runs on this host.
	FacadeAddr string `json:"facade_addr"`

	// ServingAddr is where other daemons reach this one for peer transfers
	// (UC-06). It is public, and its port is what gets announced to the
	// tracker as servingPort -- the tracker cannot infer it, because the
	// source port of our outbound HTTP connection is unrelated to it.
	ServingAddr string `json:"serving_addr"`

	// TempDir is scratch space for in-flight downloads. Ephemeral and
	// per-request: verification needs the whole file before any byte may
	// reach pkg, so a download is spooled here and deleted immediately
	// after. It is NOT a store -- the daemon has none, and serves straight
	// from the pkg cache.
	TempDir string `json:"temp_dir"`

	// CacheDir is pkg's package cache, which the daemon watches to learn
	// what it can serve. READ-ONLY: the daemon writes only to TempDir
	// and its config path. Unlike TempDir, this one is never created --
	// see Validate.
	CacheDir string `json:"cache_dir"`

	// RepoDBDir holds pkg's repository databases, one subdirectory per
	// configured repository, each containing a SQLite file named "db".
	// The daemon reads every one of them to learn each package's expected
	// hash and size (UC-02 verification, UC-05 announce sanity check).
	//
	// READ-ONLY, and more emphatically than CacheDir: these are pkg's
	// signed catalogues. The daemon opens them in read-only mode and never
	// creates this directory. Overridable for the same reason CacheDir is
	// -- so the daemon can be exercised on a non-FreeBSD box.
	RepoDBDir string `json:"repo_db_dir"`
}

// DefaultConfig returns a config with hardcoded defaults.
func DefaultConfig() *DaemonConfig {
	return &DaemonConfig{
		TrackerURL: "http://127.0.0.1:8080",
		FacadeAddr: "127.0.0.1:9001",
		// All interfaces: peers are by definition on other machines, so
		// unlike the facade this one cannot be loopback.
		ServingAddr: "0.0.0.0:9002",
		// The OS scratch directory, not a directory of our own under
		// ~/.cache. Downloads here are per-request and deleted on
		// completion; nothing needs to survive a reboot.
		TempDir: os.TempDir(),
		// FreeBSD's pkgng cache, as named in UC-05, UC-06 and the
		// use-case table. Overridable so the daemon can be exercised
		// on a non-FreeBSD box.
		CacheDir: "/var/cache/pkg",
		// Where pkg keeps its repository catalogues. Measured on
		// FreeBSD 15.1-RELEASE-p1: two repositories live here,
		// FreeBSD-ports and FreeBSD-ports-kmods, so this is a
		// directory to scan and not a single file to open.
		RepoDBDir: "/var/db/pkg/repos",
	}
}

// legacyKeys are v0.1 config keys that no longer exist. A config carrying one
// is reported rather than silently ignored: encoding/json drops unknown keys,
// so a user who had set listen_addr would otherwise get the default facade and
// serving addresses with no indication that their setting went nowhere.
var legacyKeys = map[string]string{
	"listen_addr": "replaced by facade_addr (loopback, where pkg reaches the daemon) and serving_addr (public, announced to the tracker)",
	"buffer_dir":  "renamed to temp_dir",
}

// read parses the config at path with NO side effects: nothing is created,
// moved or written, and a corrupt file is left exactly where it is. It reports
// whether the file was corrupt so Load can decide what to do about it.
//
// A missing file is not an error; it just means defaults.
//
// Fields absent from the JSON keep their default rather than becoming the zero
// value, because unmarshalling happens on top of DefaultConfig(). That is what
// lets a config written by an older build keep working: it has no cache_dir
// key, so it picks up the default instead of an empty path.
func read(path string) (cfg *DaemonConfig, corrupt bool, err error) {
	out := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing config is not an error - use defaults.
			return out, false, nil
		}
		return nil, false, fmt.Errorf("reading config: %w", err)
	}

	if err := json.Unmarshal(data, out); err != nil {
		return DefaultConfig(), true, nil
	}

	// Parsable, but possibly written against the v0.1 schema. This is an
	// error, not a corruption: the file is valid JSON and moving it aside
	// would destroy settings the user still wants, so say what to change
	// and let them change it.
	if err := checkLegacyKeys(data); err != nil {
		return nil, false, err
	}
	return out, false, nil
}

// checkLegacyKeys reports a v0.1 key by name, with what replaced it.
func checkLegacyKeys(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Not an object at all; Unmarshal into DaemonConfig already
		// classified this file, so nothing to add.
		return nil
	}
	for key, replacement := range legacyKeys {
		if _, present := raw[key]; present {
			return fmt.Errorf("config uses the removed key %q: %s", key, replacement)
		}
	}
	return nil
}

// Load is read plus the one repair the daemon performs at startup: a corrupt
// config is moved aside to .bak, preserved for inspection, and the daemon
// comes up on defaults.
//
// The rename is best effort. With no write permission on the config directory
// the daemon carries on with defaults rather than failing -- jmj never
// requires write access to its config path.
func Load(path string) (*DaemonConfig, error) {
	cfg, corrupt, err := read(path)
	if err != nil {
		return nil, err
	}
	if corrupt {
		_ = os.Rename(path, path+".bak")
	}
	return cfg, nil
}

// ServingPort is the port announced to the tracker as servingPort. It is the
// port half of ServingAddr and nothing else -- there is no derivation, no
// fallback, and no relationship to the facade's port.
func (c *DaemonConfig) ServingPort() (int, error) {
	_, portStr, err := net.SplitHostPort(c.ServingAddr)
	if err != nil {
		return 0, fmt.Errorf("serving_addr is not host:port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("serving_addr port %q is not a number: %w", portStr, err)
	}
	return port, nil
}

// validateAddr checks the "host:port" shape and the port range shared by both
// listen addresses.
func validateAddr(field, addr string) (host string, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid %s format (need host:port): %w", field, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1024 || port > 65535 {
		return "", fmt.Errorf("%s port must be 1024-65535, got %q", field, portStr)
	}
	return host, nil
}

// validateFacadeAddr enforces the loopback rule.
//
// This is mandatory, not advisory. The facade answers with package bytes on
// behalf of a mirror; exposing it off-host would let anyone on the network
// drive this daemon's fetch loop and use it as an open relay for traffic it
// pays for. An empty host is rejected for the same reason -- in Go that means
// every interface, which is the opposite of what is wanted here.
func validateFacadeAddr(addr string) error {
	host, err := validateAddr("facade_addr", addr)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("facade_addr must be a loopback address; %q listens on every interface", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("facade_addr host %q is not an IP address or \"localhost\"", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("facade_addr must be a loopback address, got %q; pkg runs on this host and nothing else may reach the facade", host)
	}
	return nil
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

	if err := validateFacadeAddr(cfg.FacadeAddr); err != nil {
		return err
	}
	// serving_addr carries no host restriction: peers are on other
	// machines, so any interface is legitimate.
	if _, err := validateAddr("serving_addr", cfg.ServingAddr); err != nil {
		return err
	}

	if cfg.TempDir == "" {
		return fmt.Errorf("temp_dir must be set")
	}
	if cfg.CacheDir == "" {
		return fmt.Errorf("cache_dir must be set (pkg's package cache, e.g. /var/cache/pkg)")
	}
	if cfg.RepoDBDir == "" {
		return fmt.Errorf("repo_db_dir must be set (pkg's repository databases, e.g. /var/db/pkg/repos)")
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

	// TempDir: must be creatable/writable. Unlike the pkg cache this one is
	// daemon-owned, so creating it is correct.
	if err := os.MkdirAll(cfg.TempDir, 0755); err != nil {
		return fmt.Errorf("temp_dir not creatable/writable: %w", err)
	}
	// MkdirAll succeeding does not prove we can write files into it, which
	// is the thing downloads actually need, so probe with the same call the
	// download path makes.
	tmp, err := os.CreateTemp(cfg.TempDir, "jmj-write-test-*")
	if err != nil {
		return fmt.Errorf("temp_dir not writable: %w", err)
	}
	tmp.Close()
	os.Remove(tmp.Name())

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

	// RepoDBDir: same rule as CacheDir and for a stronger reason. These are
	// pkg's signed repository catalogues; the daemon reads them and must
	// never create or write them. A missing directory is a misconfiguration,
	// not something to paper over -- without it the facade cannot verify a
	// single package and would 404 everything.
	info, err = os.Stat(cfg.RepoDBDir)
	if err != nil {
		return fmt.Errorf("repo_db_dir is not readable (it must already exist; the daemon never creates it): %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo_db_dir %q is not a directory", cfg.RepoDBDir)
	}

	return nil
}

// There is deliberately no Save. jmj never writes its own config file: the
// generator prints to stdout and the user redirects it wherever they have
// permission to write. That is what keeps jmj free of any privilege handling
// on the config path. Re-adding a writer would put it straight back.
