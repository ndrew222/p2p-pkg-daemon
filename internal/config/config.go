//This is the configuration library. It handles loading and validating configs.

package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

	// UpstreamURL is the conventional mirror the facade proxies to: package
	// bytes when no peer can supply them (ADR-003) and repository metadata
	// always (ADR-005). ADR-006 governs this field.
	//
	// It is the ONLY key with no default, and the only required one. Under
	// ADR-005 the facade proxies the catalogue, so this URL does not merely
	// name a fallback source -- it determines WHICH REPOSITORY pkg ends up
	// with. Defaulting it would silently choose a branch on the operator's
	// behalf, and a wrong branch does not error: pkg populates its database
	// from whatever is proxied, every hash matches, and both branches carry
	// the same signature. A setting with that consequence is stated, not
	// guessed. -generate-config refuses to emit a config without it, which is
	// what keeps "defaults are valid by construction" true (UC-01).
	//
	// May contain the literal ${ABI}, as pkg's own repository URLs do. It is
	// expanded at STARTUP and never at generation time, so a config stays
	// portable between hosts -- see ExpandUpstream.
	UpstreamURL string `json:"upstream_url"`

	// MaxConcurrentSeeds bounds how many peer transfers this daemon serves
	// at once, across all requesters (ADR-002). MaxConcurrentSeedsPerIP
	// bounds how many any single remote IP may hold. Either limit being
	// full answers 503 immediately -- no queueing, no waiting, no
	// Retry-After -- because the requester has other holders to try and
	// pkg's own mirror behind those, so a refusal is a fast fall-through
	// where a wait would be a stall.
	//
	// BOTH DEFAULT TO 0, MEANING UNLIMITED, and that is deliberate.
	// AGENTS.md asks for a real observed problem before a control of this
	// family. The hostile-peer expectation justifies building the
	// mechanism; it does not justify a number, and nobody has measured
	// one. At 0 the default behaviour is unchanged and an operator opts in.
	//
	// The key names are an owner ruling of 2026-08-09, recorded at
	// HANDOFF.md §4.7 -- ADR-002 itself left them unnamed. Do not rename
	// them.
	//
	// Negative is a configuration error rather than a synonym for
	// unlimited: 0 already says that, and silently accepting -1 would hide
	// a typo in an arithmetic expression that produced it.
	MaxConcurrentSeeds      int `json:"max_concurrent_seeds"`
	MaxConcurrentSeedsPerIP int `json:"max_concurrent_seeds_per_ip"`
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
		// Both seeding caps default to 0 = unlimited (ADR-002). Unlike
		// UpstreamURL this default is safe to state, because it is the
		// behaviour the daemon already had before the caps existed.
		MaxConcurrentSeeds:      0,
		MaxConcurrentSeedsPerIP: 0,
		// UpstreamURL is deliberately absent. It is the one field with no
		// default: see its doc comment and ADR-006. Leaving it empty here
		// is what makes -generate-config fail without -upstream, which is
		// the mechanism that keeps every emitted config startable.
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

	if err := validateUpstreamURL(cfg.UpstreamURL); err != nil {
		return err
	}

	// ADR-002's two seeding caps. 0 is unlimited; anything below that is a
	// mistake, not a stronger form of unlimited.
	if cfg.MaxConcurrentSeeds < 0 {
		return fmt.Errorf("max_concurrent_seeds must be >= 0 (0 means unlimited), got %d", cfg.MaxConcurrentSeeds)
	}
	if cfg.MaxConcurrentSeedsPerIP < 0 {
		return fmt.Errorf("max_concurrent_seeds_per_ip must be >= 0 (0 means unlimited), got %d", cfg.MaxConcurrentSeedsPerIP)
	}
	// A per-IP cap above the global one can never fire, so it is almost
	// certainly a transposition of the two values. Not fatal -- the
	// combination is well defined and the global cap simply binds first --
	// but worth reporting, because ADR-002 requires diagnostics good enough
	// to tell an attack from a misconfigured ceiling. See Warnings.

	return nil
}

// ABIPlaceholder is the one variable expanded inside upstream_url, spelled as
// pkg spells it in its own repository URLs. pkg may support others; that set
// has not been measured, so anything else is left alone and caught by the
// unexpanded-placeholder check in ExpandUpstream rather than mis-expanded.
const ABIPlaceholder = "${ABI}"

// validateUpstreamURL judges upstream_url from the value alone (ADR-006).
//
// Required, because it decides which repository pkg ends up with and has no
// safe default. Note that ${ABI} survives url.ParseRequestURI as an ordinary
// path segment, so a config carrying the placeholder validates here and is
// resolved later against the host -- which is the whole point of the split.
func validateUpstreamURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("upstream_url must be set: it is the mirror the facade proxies to for metadata (ADR-005) and for packages no peer holds (ADR-003), and it has no default because it decides which repository pkg installs from. Pass -upstream, e.g. -upstream https://pkg.FreeBSD.org/${ABI}/quarterly")
	}

	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("invalid upstream_url %q: %w", raw, err)
	}

	// "pkg+https://..." is the form in pkg's own config and the obvious
	// thing to paste. Reported rather than silently stripped, on the same
	// principle as legacyKeys: a wrong config is named, not reinterpreted.
	if rest, found := strings.CutPrefix(u.Scheme, "pkg+"); found {
		return fmt.Errorf("upstream_url %q carries pkg's %q scheme prefix; jmj wants a plain URL an HTTP client accepts -- drop the \"pkg+\" and use %q", raw, u.Scheme, rest+"://"+u.Host+u.Path)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("upstream_url scheme must be http or https, got %q in %q", u.Scheme, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("upstream_url %q has no host", raw)
	}

	return nil
}

// Warnings are non-fatal observations about a valid config, returned rather
// than logged so that ValidateFields stays free of side effects and the
// generator keeps stdout clean for the redirect. Callers log them to stderr.
func Warnings(cfg *DaemonConfig) []string {
	var out []string
	// Plaintext upstream is permitted, unlike a non-loopback facade_addr
	// which is refused. The asymmetry is deliberate (ADR-006): a non-loopback
	// facade is an open relay, whereas plaintext upstream is not an integrity
	// hole -- pkg verifies the catalogue signature and jmj verifies package
	// hashes, so tampering is caught either way. What is lost is privacy and
	// early detection, which is warning-shaped.
	if u, err := url.ParseRequestURI(cfg.UpstreamURL); err == nil && u.Scheme == "http" {
		out = append(out, fmt.Sprintf("upstream_url %q is plaintext http; prefer https. Tampering is still caught (pkg checks the catalogue signature, jmj checks package hashes), but the transfer is readable in transit", cfg.UpstreamURL))
	}
	// A per-IP cap that cannot bind is dead configuration: the global cap
	// refuses first in every case. Reported rather than corrected, because
	// which of the two numbers the operator meant is not ours to guess.
	if cfg.MaxConcurrentSeeds > 0 && cfg.MaxConcurrentSeedsPerIP > cfg.MaxConcurrentSeeds {
		out = append(out, fmt.Sprintf("max_concurrent_seeds_per_ip (%d) exceeds max_concurrent_seeds (%d), so the per-IP cap can never fire; the two values may be transposed",
			cfg.MaxConcurrentSeedsPerIP, cfg.MaxConcurrentSeeds))
	}
	return out
}

// ABIFunc reports the host's pkg ABI, e.g. "FreeBSD:15:amd64". Injected so the
// expansion can be tested off FreeBSD.
type ABIFunc func() (string, error)

// PkgABI asks pkg for the host's ABI.
//
// This is the daemon's one execution of an external binary, permitted by
// ADR-006 and kept narrow on purpose: one command, one question, and only
// when upstream_url actually carries the placeholder. Reading pkg's files was
// already precedent; this asks it a question and still never modifies it.
func PkgABI() (string, error) {
	out, err := exec.Command("pkg", "config", "abi").Output()
	if err != nil {
		return "", fmt.Errorf("could not run \"pkg config abi\": %w", err)
	}
	abi := strings.TrimSpace(string(out))
	if abi == "" {
		return "", fmt.Errorf("\"pkg config abi\" returned nothing")
	}
	return abi, nil
}

// ExpandUpstream resolves ${ABI} in upstream_url against THIS host.
//
// Startup-only, never generation-time: UC-01 step 2 guarantees the generator
// touches no filesystem so a config can be written on one machine for another,
// which means the emitted config keeps the literal ${ABI} and this resolves it
// where the daemon actually runs (UC-01 step 7's "validate against this
// machine" phase).
//
// abi is called ONLY when the placeholder is present, so a literal URL never
// shells out and the daemon stays exercisable on a non-FreeBSD box -- the same
// reason cache_dir and repo_db_dir are overridable. Failure is fatal: proxying
// from a URL with an unexpanded placeholder in it is the bad outcome.
func ExpandUpstream(cfg *DaemonConfig, abi ABIFunc) error {
	if strings.Contains(cfg.UpstreamURL, ABIPlaceholder) {
		v, err := abi()
		if err != nil {
			return fmt.Errorf("upstream_url %q needs %s expanded, but the host ABI could not be determined: %w", cfg.UpstreamURL, ABIPlaceholder, err)
		}
		cfg.UpstreamURL = strings.ReplaceAll(cfg.UpstreamURL, ABIPlaceholder, v)
	}

	// Catches a variable we do not expand (pkg may support others; that set
	// is unmeasured). Better to refuse than to proxy from a URL with a
	// literal "${...}" in the path.
	if i := strings.Index(cfg.UpstreamURL, "${"); i >= 0 {
		return fmt.Errorf("upstream_url %q still carries an unexpanded placeholder at offset %d; only %s is expanded", cfg.UpstreamURL, i, ABIPlaceholder)
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
