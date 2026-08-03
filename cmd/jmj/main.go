package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/ndrew222/p2p-pkg-daemon/internal/daemon"
)

func main() {
	var (
		id       = flag.String("id", "", "peer ID")
		addr     = flag.String("addr", "", "listen address")
		cacheDir = flag.String("cachedir", "", "cache directory")
	)
	flag.Parse()

	if *id == "" || *addr == "" || *cacheDir == "" {
		fmt.Fprintln(os.Stderr, "Usage: jmj -id <peerID> -addr <host:port> -cachedir <dir>")
		os.Exit(1)
	}

	// UC-05 requires the listening port to travel alongside every
	// announce, because the tracker can see our IP from the connection
	// itself but has no way to guess which port we're listening on. -addr
	// is "host:port"; pull just the port number out of it here.
	_, portString, err := net.SplitHostPort(*addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -addr %q, expected host:port: %v\n", *addr, err)
		os.Exit(1)
	}
	listeningPort, err := strconv.Atoi(portString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid port in -addr %q: %v\n", *addr, err)
		os.Exit(1)
	}

	cw := daemon.New(*cacheDir, listeningPort, nil,
		func(port int, pkgs []daemon.PackageInfo) {
			fmt.Printf("[update] port=%d %d packages\n", port, len(pkgs))
			for _, p := range pkgs {
				fmt.Printf("  %s  (%d bytes)\n", p.NameVersion(), p.FileSizeBytes)
			}
		},
		func(ev daemon.ChangeEvent) {
			fmt.Printf("[change] %s: %s  (%d bytes)\n", ev.Type, ev.Package.NameVersion(), ev.Package.FileSizeBytes)
		},
	)

	if err := cw.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start cache watcher: %v\n", err)
		os.Exit(1)
	}
	defer cw.Stop()

	// Initial scan
	if _, err := cw.Scan(); err != nil {
		fmt.Fprintf(os.Stderr, "initial scan failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("jmj running: id=%s addr=%s cachedir=%s\n", *id, *addr, *cacheDir)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("shutting down...")
}
