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

	if err := d.startHTTPServerLocked(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}
	if err := d.startDiscoveryLocked(); err != nil {
		return fmt.Errorf("failed to start discovery: %w", err)
	}

	d.setupReloadHandler()
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

	// repoDB is nil: no RepositoryDatabase implementation exists yet, so
	// SanityFilter degrades to filename-format checks and skips the size
	// comparison. Safe but weaker -- a truncated package can be announced,
	// costing one wasted transfer, which the end-to-end hash check on the
	// downloader's side catches. See the work log.
	d.watcher = New(d.config.CacheDir, nil, nil, onChange)
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

	// Deliberately empty. The /ping handler that used to be here was an
	// invented health endpoint appearing in no spec.
	//
	// TODO: the mirror facade (Facade, UC-02/UC-07) and the peer seed
	// server (peer.Server, UC-06) both still need mounting. Neither can go
	// on this mux: the facade must be loopback-only on its own port, and
	// peer.Server speaks the peerwire framing, not HTTP. Both are blocked
	// on the repository database, which nothing implements yet.
	mux := http.NewServeMux()

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

	d.config = newCfg

	if facadeChanged {
		if err := d.startHTTPServerLocked(); err != nil {
			return fmt.Errorf("failed to restart HTTP server: %w", err)
		}
		log.Printf("HTTP server restarted on %s", d.config.FacadeAddr)
	}

	// A serving address change means the tracker is advertising a stale
	// port for us, so discovery has to re-announce. A facade change does
	// not: nothing about the facade's port reaches the tracker. A cache
	// change means the watcher is watching the wrong directory.
	if trackerChanged || servingChanged || cacheChanged {
		d.stopDiscoveryLocked()
		if err := d.startDiscoveryLocked(); err != nil {
			return fmt.Errorf("failed to restart discovery: %w", err)
		}
		log.Printf("Discovery re-announced to %s", d.config.TrackerURL)
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
