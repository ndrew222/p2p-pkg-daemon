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
	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

type Daemon struct {
	mu         sync.Mutex
	config     *config.DaemonConfig
	configPath string
	peerID     string
	cids       []string
	client     *discovery.Client
	httpServer *http.Server
	running    bool
}

var (
	globalDaemon *Daemon
	daemonMu     sync.Mutex
)

// Start initializes and runs the daemon with the given config
func Start(cfg *config.DaemonConfig, peerID string, cids []string, configPath string) error {
	d := &Daemon{
		config:     cfg,
		configPath: configPath,
		peerID:     peerID,
		cids:       cids,
		running:    true,
	}

	// Set global
	daemonMu.Lock()
	globalDaemon = d
	daemonMu.Unlock()

	// Start HTTP server
	if err := d.startHTTPServer(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	// Start discovery client with heartbeat
	if err := d.startDiscovery(); err != nil {
		return fmt.Errorf("failed to start discovery: %w", err)
	}

	// Setup SIGHUP for hot reload
	d.setupReloadHandler()

	return nil
}

func (d *Daemon) startDiscovery() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	client := discovery.New(d.config.TrackerURL, proto.PeerID(d.peerID), d.config.ListenAddr)
	if err := client.Announce(d.cids); err != nil {
		return fmt.Errorf("initial announce failed: %w", err)
	}

	d.client = client

	// Start heartbeat in background
	getCIDs := func() []string { return d.cids }
	go client.RunHeartbeat(getCIDs)

	log.Printf("Discovery started: peer=%s, tracker=%s, addr=%s",
		d.peerID, d.config.TrackerURL, d.config.ListenAddr)
	return nil
}

func (d *Daemon) startHTTPServer() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.httpServer != nil {
		// Close old server if it exists
		d.httpServer.Close()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// TODO: UC-06 will add /download, /serve endpoints

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

// Reload reloads the config from file and applies changes without restarting
func (d *Daemon) Reload() error {
	// Read and validate while holding the lock, then release it before calling
	// startHTTPServer/startDiscovery: those lock d.mu themselves, and sync.Mutex
	// is not reentrant (holding it here would deadlock).
	d.mu.Lock()
	newCfg, err := config.Load(d.configPath)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate
	if err := config.Validate(newCfg); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("invalid config: %w", err)
	}

	// Check if address changed
	addrChanged := d.config.ListenAddr != newCfg.ListenAddr
	trackerChanged := d.config.TrackerURL != newCfg.TrackerURL

	// Update config
	d.config = newCfg
	d.mu.Unlock()

	// If address changed, restart HTTP server
	if addrChanged {
		if err := d.startHTTPServer(); err != nil {
			return fmt.Errorf("failed to restart HTTP server: %w", err)
		}
		log.Printf("HTTP server restarted on %s", d.config.ListenAddr)
	}

	// If tracker changed, restart discovery
	if trackerChanged || addrChanged {
		// Stop old client (needs Stop() method on discovery.Client)
		if d.client != nil {
			d.client.Stop()
		}
		if err := d.startDiscovery(); err != nil {
			return fmt.Errorf("failed to restart discovery: %w", err)
		}
		log.Printf("Discovery re-announced to %s", d.config.TrackerURL)
	}

	return nil
}

// Stop gracefully shuts down the daemon
func (d *Daemon) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.running = false

	if d.httpServer != nil {
		d.httpServer.Close()
	}

	if d.client != nil {
		d.client.Stop()
	}

	return nil
}
