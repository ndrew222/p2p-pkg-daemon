package daemon

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/ndrew222/p2p-pkg-daemon/internal/config"
	"github.com/ndrew222/p2p-pkg-daemon/internal/discovery"
)

type Daemon struct {
	mu         sync.Mutex
	config     *config.DaemonConfig
	configPath string
	packages   []string
	client     *discovery.Client
	done       chan struct{} // closed to stop the keep-alive loop
	httpServer *http.Server
	running    bool
}

var (
	globalDaemon *Daemon
	daemonMu     sync.Mutex
)

// staticCache reports a fixed package list to the keep-alive.
//
// PLACEHOLDER. The real source is the cache watcher (Watcher.Scan plus its
// change channel), which is written but not yet wired into the daemon --
// see the work log. Until it is, the daemon announces whatever list it was
// started with and never notices a pkg install or pkg clean.
type staticCache struct {
	packages []string
}

func (s staticCache) Scan() ([]string, error) { return s.packages, nil }

// Start initializes and runs the daemon with the given config.
func Start(cfg *config.DaemonConfig, packages []string, configPath string) error {
	d := &Daemon{
		config:     cfg,
		configPath: configPath,
		packages:   packages,
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

// servingPort extracts the port a peer would dial us on.
//
// PROVISIONAL. v0.2 has the daemon announce the port it listens on for peer
// transfers, which is not necessarily the port it serves HTTP on -- and the
// mirror facade needs a third, loopback-only port that config does not have
// either. config.DaemonConfig carries a single ListenAddr, so this reuses it,
// which is exactly what the pre-v0.2 code did when it announced ListenAddr as
// its address. Splitting the ports is a config change and an open question for
// the spec owner; see the work log.
func servingPort(listenAddr string) (int, error) {
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return 0, fmt.Errorf("listen_addr is not host:port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("listen_addr port %q is not a number: %w", portStr, err)
	}
	return port, nil
}

// startDiscoveryLocked starts the tracker conversation. CALLER MUST HOLD d.mu.
func (d *Daemon) startDiscoveryLocked() error {
	port, err := servingPort(d.config.ListenAddr)
	if err != nil {
		return err
	}

	d.client = discovery.New(d.config.TrackerURL)
	d.done = make(chan struct{})

	// KeepAlive owns the announce/ping loop, including the initial
	// registration and the rule that we stay quiet while nothing is
	// registered. A tracker that is down at startup is no longer fatal:
	// the loop keeps trying, which is the self-healing behaviour v0.2
	// describes. The old code failed Start() on the first announce error.
	ka := discovery.NewKeepAlive(d.client, staticCache{packages: d.packages}, port, nil)
	go ka.Run(d.done)

	log.Printf("Discovery started: tracker=%s, servingPort=%d, packages=%d",
		d.config.TrackerURL, port, len(d.packages))
	return nil
}

// stopDiscoveryLocked stops the keep-alive loop. CALLER MUST HOLD d.mu.
// Safe to call when discovery was never started or is already stopped.
func (d *Daemon) stopDiscoveryLocked() {
	if d.done == nil {
		return
	}
	close(d.done)
	d.done = nil // so a second call does not close a closed channel
}

// startHTTPServerLocked (re)starts the daemon's HTTP listener.
// CALLER MUST HOLD d.mu.
func (d *Daemon) startHTTPServerLocked() error {
	if d.httpServer != nil {
		d.httpServer.Close()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// TODO: the mirror facade (Facade, UC-02/UC-07) and the peer seed
	// server (peer.Server, UC-06) both still need mounting. Neither can go
	// on this mux: the facade must be loopback-only on its own port, and
	// peer.Server speaks the peerwire framing, not HTTP. Both are blocked
	// on the config change noted in servingPort.

	d.httpServer = &http.Server{
		Addr:    d.config.ListenAddr,
		Handler: mux,
	}

	go func() {
		log.Printf("HTTP server listening on %s", d.config.ListenAddr)
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

	addrChanged := d.config.ListenAddr != newCfg.ListenAddr
	trackerChanged := d.config.TrackerURL != newCfg.TrackerURL

	d.config = newCfg

	if addrChanged {
		if err := d.startHTTPServerLocked(); err != nil {
			return fmt.Errorf("failed to restart HTTP server: %w", err)
		}
		log.Printf("HTTP server restarted on %s", d.config.ListenAddr)
	}

	// The serving port is derived from ListenAddr, so an address change
	// means the tracker is advertising a stale port for us and discovery
	// has to restart too.
	if trackerChanged || addrChanged {
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
