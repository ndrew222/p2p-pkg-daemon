package daemon

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ndrew222/p2p-pkg-daemon/internal/config"
	"github.com/ndrew222/p2p-pkg-daemon/internal/discovery"
)

type Daemon struct {
	mu         sync.Mutex
	config     *config.DaemonConfig
	configPath string
	client     *discovery.Client
	watcher    *Watcher
	repo       *Repositories
	done       chan struct{} // closed to stop the keep-alive loop
	httpServer *http.Server
	running    bool
}

var (
	globalDaemon *Daemon
	daemonMu     sync.Mutex
)

// cacheSource adapts the cache watcher to discovery.Cache. The keep-alive
// wants the name-version strings that go on the wire; the watcher deals in
// PackageInfo. SanityFilter has already dropped anything without both a name
// and a version, so every entry here has a real name-version.
type cacheSource struct {
	watcher *Watcher
}

func (c cacheSource) Scan() ([]string, error) {
	pkgs, err := c.watcher.Scan()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.NameVersion())
	}
	return out, nil
}

// Start initializes and runs the daemon with the given config.
func Start(cfg *config.DaemonConfig, configPath string) error {
	d := &Daemon{
		config:     cfg,
		configPath: configPath,
		running:    true,
	}

	daemonMu.Lock()
	globalDaemon = d
	daemonMu.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Order matters now. The repository snapshot is what the watcher
	// sanity-checks sizes against and what the facade verifies hashes from,
	// and the facade also needs the discovery client, which
	// startDiscoveryLocked creates. So: catalogue, then discovery, then the
	// listener that depends on both.
	if err := d.openRepositoriesLocked(); err != nil {
		return fmt.Errorf("failed to read the repository database: %w", err)
	}
	if err := d.startDiscoveryLocked(); err != nil {
		return fmt.Errorf("failed to start discovery: %w", err)
	}
	if err := d.startHTTPServerLocked(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	d.setupReloadHandler()
	return nil
}

// repository returns the catalogue as an interface value, or a genuinely nil
// interface when there is none.
//
// This exists to avoid a trap rather than to add a layer. Assigning a nil
// *Repositories straight into an interface field produces a non-nil interface
// holding a nil pointer, so every "== nil" check downstream passes and the
// first method call panics on the nil receiver -- which would defeat both
// SanityFilter's nil-means-skip branch and Facade.Check's refusal to serve
// without a catalogue. Production always has one; the nil path exists for
// callers that construct a Daemon directly.
func (d *Daemon) repository() Repository {
	if d.repo == nil {
		return nil
	}
	return d.repo
}

// openRepositoriesLocked loads pkg's repository catalogues.
// CALLER MUST HOLD d.mu.
//
// Failure is fatal to startup, deliberately. Without a catalogue the daemon
// cannot verify a single package: the facade would answer 404 to everything and
// the announce path would advertise packages whose size it never checked. That
// is a misconfiguration to report, not a degraded mode to run in.
func (d *Daemon) openRepositoriesLocked() error {
	repo, err := OpenRepositories(d.config.RepoDBDir)
	if err != nil {
		return err
	}
	d.repo = repo
	log.Printf("Repository database: %d packages from %s", repo.Len(), d.config.RepoDBDir)

	// Advisory only (ADR-006): warns, never refuses. See upstreamcheck.go
	// for why a silent branch mismatch is otherwise undetectable.
	for _, w := range UpstreamWarnings(d.config.UpstreamURL, repo.Sources()) {
		log.Printf("Warning: %s", w)
	}
	return nil
}

// startDiscoveryLocked starts the cache watcher and the tracker conversation.
// CALLER MUST HOLD d.mu.
func (d *Daemon) startDiscoveryLocked() error {
	// The announced port is the serving address's port, read straight off
	// the config. The provisional derivation that used to live here -- one
	// listen address doing duty for all three ports -- is gone with the
	// two-address schema.
	port, err := d.config.ServingPort()
	if err != nil {
		return err
	}

	// changed is the watcher's nudge to the keep-alive. Buffered with room
	// for one, and the send below is non-blocking, which does two things:
	// the watcher's event loop can never block on a keep-alive that is
	// mid-announce, and a burst of events (installing one package pulls in
	// dozens of dependencies, each firing an fsnotify event) collapses into
	// a single pending re-announce instead of dozens.
	changed := make(chan struct{}, 1)
	onChange := func(ChangeEvent) {
		select {
		case changed <- struct{}{}:
		default: // a re-announce is already pending; it will pick this up too
		}
	}

	// The watcher now has a real repository database, so SanityFilter does
	// the size comparison it was written for: a cached file whose size does
	// not match the catalogue is not announced. Announcing a truncated
	// package used to cost a peer one wasted transfer before its hash check
	// caught it.
	d.watcher = New(d.config.CacheDir, d.repository(), nil, onChange)
	if err := d.watcher.Start(); err != nil {
		return fmt.Errorf("cache watcher: %w", err)
	}

	d.client = discovery.New(d.config.TrackerURL)
	d.done = make(chan struct{})

	// KeepAlive owns the announce/ping loop, including the initial
	// registration and the rule that we stay quiet while nothing is
	// registered. A tracker that is down at startup is not fatal: the loop
	// keeps trying, which is the self-healing behaviour v0.2 describes.
	ka := discovery.NewKeepAlive(d.client, cacheSource{watcher: d.watcher}, port, changed)
	go ka.Run(d.done)

	log.Printf("Discovery started: tracker=%s, servingPort=%d, cache=%s",
		d.config.TrackerURL, port, d.config.CacheDir)
	return nil
}

// stopDiscoveryLocked stops the keep-alive loop and the cache watcher.
// CALLER MUST HOLD d.mu. Safe to call when discovery was never started or is
// already stopped.
func (d *Daemon) stopDiscoveryLocked() {
	if d.done != nil {
		close(d.done)
		d.done = nil // so a second call does not close a closed channel
	}
	if d.watcher != nil {
		d.watcher.Stop()
		d.watcher = nil
	}
}

// startHTTPServerLocked (re)starts the daemon's HTTP listener.
// CALLER MUST HOLD d.mu.
func (d *Daemon) startHTTPServerLocked() error {
	if d.httpServer != nil {
		d.httpServer.Close()
	}

	// The mirror facade takes the root. pkg addresses a mirror with whatever
	// repository path its config produces, so the facade cannot be mounted
	// under a fixed prefix; packageRequest is what decides which of those
	// paths are package files and which are metadata (UC-02, UC-07).
	//
	// The peer seed server (peer.Server, UC-06) still goes on serving_addr
	// separately: it speaks the peerwire framing, not HTTP, until the peer
	// wire migration lands.
	//
	// A restart rebuilds the facade, which resets its in-memory peer
	// blacklist. That is accepted: the list is local, unpersisted and
	// advisory, so the cost is at most one wasted transfer per bad peer,
	// and end-to-end hash verification -- not the blacklist -- is what makes
	// corrupt bytes impossible.
	facade := &Facade{
		Peers:   d.client,
		Repo:    d.repository(),
		TempDir: d.config.TempDir,
	}
	if err := facade.Check(); err != nil {
		return fmt.Errorf("mirror facade: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", facade)

	d.httpServer = &http.Server{
		Addr:    d.config.FacadeAddr,
		Handler: mux,
	}

	go func() {
		log.Printf("HTTP server listening on %s", d.config.FacadeAddr)
		if err := d.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

func (d *Daemon) setupReloadHandler() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP)

	go func() {
		for range sigChan {
			log.Println("SIGHUP received, reloading config...")
			if err := d.Reload(); err != nil {
				log.Printf("Reload failed: %v", err)
			} else {
				log.Println("Config reloaded successfully")
			}
		}
	}()
}

// Reload reloads the config from file and applies changes without restarting.
func (d *Daemon) Reload() error {
	newCfg, err := config.Load(d.configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := config.Validate(newCfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	// SIGHUP goes through the same host-resolution step startup does, or a
	// reloaded config could leave a literal ${ABI} in the upstream URL
	// (ADR-006). Only runs pkg when the placeholder is actually present.
	if err := config.ExpandUpstream(newCfg, config.PkgABI); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	for _, w := range config.Warnings(newCfg) {
		log.Printf("Warning: %s", w)
	}

	// Take the lock only once, and call the Locked helpers from under it.
	// The previous version locked here and then called startHTTPServer,
	// which locked again -- sync.Mutex is not reentrant, so any SIGHUP
	// that changed the listen address deadlocked the daemon.
	d.mu.Lock()
	defer d.mu.Unlock()

	// The two addresses now move independently: facade_addr is the local
	// listener, serving_addr is what the tracker advertises for us.
	facadeChanged := d.config.FacadeAddr != newCfg.FacadeAddr
	servingChanged := d.config.ServingAddr != newCfg.ServingAddr
	trackerChanged := d.config.TrackerURL != newCfg.TrackerURL
	cacheChanged := d.config.CacheDir != newCfg.CacheDir
	repoDBChanged := d.config.RepoDBDir != newCfg.RepoDBDir

	d.config = newCfg

	// Rebuild in dependency order, the same order Start uses: the catalogue
	// feeds the watcher, and the listener depends on both it and the client.
	if repoDBChanged {
		if err := d.openRepositoriesLocked(); err != nil {
			return fmt.Errorf("failed to reload the repository database: %w", err)
		}
	}

	// A serving address change means the tracker is advertising a stale
	// port for us, so discovery has to re-announce. A facade change does
	// not: nothing about the facade's port reaches the tracker. A cache
	// change means the watcher is watching the wrong directory, and a
	// repository change means it is sanity-checking against the wrong sizes.
	if trackerChanged || servingChanged || cacheChanged || repoDBChanged {
		d.stopDiscoveryLocked()
		if err := d.startDiscoveryLocked(); err != nil {
			return fmt.Errorf("failed to restart discovery: %w", err)
		}
		log.Printf("Discovery re-announced to %s", d.config.TrackerURL)
	}

	// The facade holds the discovery client and the repository snapshot, so
	// it goes stale when either is replaced -- not only when its own address
	// moves. A tracker change without this would leave the facade asking the
	// previous tracker who holds a package.
	if facadeChanged || trackerChanged || repoDBChanged {
		if err := d.startHTTPServerLocked(); err != nil {
			return fmt.Errorf("failed to restart HTTP server: %w", err)
		}
		log.Printf("HTTP server restarted on %s", d.config.FacadeAddr)
	}

	return nil
}

// Stop gracefully shuts down the daemon.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.running = false

	if d.httpServer != nil {
		d.httpServer.Close()
	}
	d.stopDiscoveryLocked()

	return nil
}
