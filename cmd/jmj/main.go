package main

import (
	"encoding/json"
	"flag" // command line arg
	"fmt"
	"log" // output
	"os"
	"path/filepath"

	"github.com/ndrew222/p2p-pkg-daemon/internal/config" // UC-01
	"github.com/ndrew222/p2p-pkg-daemon/internal/daemon" // UC-01
)

// TEMPORARY STUB: exists only to exercise internal/discovery end-to-end
// Owner of cmd/jmj: replace freely

func main() {
	// each flags below returns a *string, as command line hasnt been read yet, pointer points to those empty slots to be filled later
	// ---- Define flags ----
	var (
		tracker    = flag.String("tracker", "", "Tracker URL (overrides config)")
		addr       = flag.String("addr", "", "Listen address (overrides config)")
		buffer     = flag.String("buffer", "", "Buffer directory (overrides config)")
		cache      = flag.String("cache", "", "pkg cache directory, read-only (overrides config)")
		configPath = flag.String("config", "", "Path to config file (default: $HOME/.config/jmj/config.json)")
		genConfig  = flag.Bool("generate-config", false, "Generate config JSON to stdout and exit")
	// reads os.Args and fill those slots in
	)
	flag.Parse()

	// Determine config file path
	if *configPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			*configPath = filepath.Join(home, ".config", "jmj", "config.json")
		} else {
			*configPath = "./config.json" // fallback (shouldn't happen)
		}
	}

	// ---- GENERATOR MODE ----
	if *genConfig {
		cfg := config.DefaultConfig()
		if *tracker != "" {
			cfg.TrackerURL = *tracker
		}
		if *addr != "" {
			cfg.ListenAddr = *addr
		}
		if *buffer != "" {
			cfg.BufferDir = *buffer
		}
		if *cache != "" {
			cfg.CacheDir = *cache
		}
		// Fields only: the generator writes a config for whatever host
		// will run it, so it must not demand that this host already has
		// the pkg cache or be willing to create the buffer directory.
		if err := config.ValidateFields(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to encode config: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// ---- DAEMON MODE ----
	// No required flags any more. There is no -id (v0.2 gives the daemon no
	// identity to send; the tracker keys on the connection's source IP) and
	// no -packages (the cache watcher discovers the list from cache_dir, so
	// a hand-written one would just go stale on the first pkg install).
	//
	// An empty cache is a legitimate state: the keep-alive stays quiet until
	// there is something to announce.

	// 1. Load config (missing → defaults, corrupt → .bak + defaults)
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	// 2. Merge flag overrides (only if flag was explicitly provided)
	if *tracker != "" {
		cfg.TrackerURL = *tracker
	}
	if *addr != "" {
		cfg.ListenAddr = *addr
	}
	if *buffer != "" {
		cfg.BufferDir = *buffer
	}
	if *cache != "" {
		cfg.CacheDir = *cache
	}

	// 3. Validate merged config
	if err := config.Validate(cfg); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	// Start daemon (this handles everything: cache watcher, client,
	// keep-alive, HTTP server, SIGHUP)
	if err := daemon.Start(cfg, *configPath); err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}

	// ---- BLOCK FOREVER (keep daemon running) ----
	// daemon.Start already launched the keep-alive goroutine
	// (discovery.KeepAlive.Run), so main just parks here.
	log.Println("jmj daemon is running. Press Ctrl+C to stop.")
	select {} // blocks indefinitely
}
