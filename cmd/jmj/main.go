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

// applyOverrides copies the flags the user actually set onto cfg. An empty
// string means the flag was not given, so the config (or its default) stands.
// Shared by both modes so the generator and the daemon can never drift on
// which flags they honour.
func applyOverrides(cfg *config.DaemonConfig, tracker, facadeAddr, servingAddr, buffer, cache string) {
	if tracker != "" {
		cfg.TrackerURL = tracker
	}
	if facadeAddr != "" {
		cfg.FacadeAddr = facadeAddr
	}
	if servingAddr != "" {
		cfg.ServingAddr = servingAddr
	}
	if buffer != "" {
		cfg.BufferDir = buffer
	}
	if cache != "" {
		cfg.CacheDir = cache
	}
}

func main() {
	// each flags below returns a *string, as command line hasnt been read yet, pointer points to those empty slots to be filled later
	// ---- Define flags ----
	var (
		tracker    = flag.String("tracker", "", "Tracker URL (overrides config)")
		facadeAddr = flag.String("facade-addr", "", "Loopback address pkg reaches the mirror facade on (overrides config)")
		servingArg = flag.String("serving-addr", "", "Address peers reach this daemon on; its port is announced to the tracker (overrides config)")
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
	//
	// Generates a config and prints it. That is the whole job: nothing is
	// created, moved or written, so jmj needs no write permission anywhere
	// and there is no permission handling to get wrong. The user redirects
	// the output to wherever they can write:
	//
	//	jmj -generate-config -tracker http://10.0.0.1:8080 > config.json
	//	jmj -generate-config | sudo tee /usr/local/etc/jmj.json
	if *genConfig {
		// Defaults plus whatever the user asked for. The existing config
		// is deliberately NOT read as a merge base: the usual invocation
		// redirects onto that very file, and the shell truncates a
		// redirect target before jmj is even started, so the "existing"
		// config would already be an empty file. Flags are how you say
		// what you want; defaults are valid by construction.
		cfg := config.DefaultConfig()

		applyOverrides(cfg, *tracker, *facadeAddr, *servingArg, *buffer, *cache)

		// Fields only: the generator produces a config for whatever host
		// will run the daemon, so it must not demand that THIS host
		// already has the pkg cache, and must not create anything.
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
	applyOverrides(cfg, *tracker, *facadeAddr, *servingArg, *buffer, *cache)

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
