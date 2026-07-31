package main

import (
	"encoding/json"
	"flag" // command line arg
	"fmt"
	"log" // output
	"os"
	"path/filepath"
	"strings" // strings.Split to turn "aaa, bbb, ccc" into ["aaa", "bbb", "ccc"]

	"github.com/ndrew222/p2p-pkg-daemon/internal/config" // UC-01
	"github.com/ndrew222/p2p-pkg-daemon/internal/daemon" // UC-01
	//"github.com/ndrew222/p2p-pkg-daemon/internal/discovery" // F3 client
	//"github.com/ndrew222/p2p-pkg-daemon/internal/proto"     // needed only for proto.PeerID type conversation
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
		peerID     = flag.String("id", "", "Peer ID (required, not persisted)")
		cidList    = flag.String("cids", "", "Comma-separated CIDs we hold (required, not persisted)")
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
		if err := config.Validate(cfg); err != nil {
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
	// Required flags: -id and -cids (these are not persisted)
	// flag check
	// os.Exit(1) for daemon with no identity as it cant ping and cant be found
	// fail here early
	if *peerID == "" || *cidList == "" {
		log.Fatal("jmj: -id and -cids  are required")
	}
	cids := strings.Split(*cidList, ",")

	// 1. Fail fast: validate requested settings BEFORE touching the config file.
	//    Per UC-01, invalid args must leave the config file untouched.
	candidate := config.DefaultConfig()
	applyOverrides(candidate, *tracker, *addr, *buffer)
	if err := config.Validate(candidate); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	// 2. Load config (missing → defaults, corrupt → .bak + defaults)
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	// 3. Merge flag overrides (only if flag was explicitly provided)
	applyOverrides(cfg, *tracker, *addr, *buffer)

	// 4. Validate merged config (catches invalid values from the config file)
	if err := config.Validate(cfg); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	// Start daemon (this handles everything: client, heartbeat, HTTP server, SIGHUP)
	if err := daemon.Start(cfg, *peerID, cids, *configPath); err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}

	// ---- BLOCK FOREVER (keep daemon running) ----
	log.Println("jmj daemon is running. Press Ctrl+C to stop.")
	select {} // blocks indefinitely
}

// applyOverrides sets config fields only for flags the user explicitly provided.
func applyOverrides(cfg *config.DaemonConfig, tracker, addr, buffer string) {
	if tracker != "" {
		cfg.TrackerURL = tracker
	}
	if addr != "" {
		cfg.ListenAddr = addr
	}
	if buffer != "" {
		cfg.BufferDir = buffer
	}
}
