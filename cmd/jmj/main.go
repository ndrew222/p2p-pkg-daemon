package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
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

	// TODO: pass a real RepositoryDatabase implementation here once we have
	// read access to pkg's repo DB wired up. Passing nil for now means
	// SanityFilter only checks that file names look valid; it can't yet
	// check file sizes against the repo DB.
	cw := cachewatcher.New(*cacheDir, nil,
		func(pkgs []cachewatcher.PackageInfo) {
			fmt.Printf("[update] %d packages\n", len(pkgs))
			for _, p := range pkgs {
				fmt.Printf("  %s  (%d bytes)\n", p.NameVersion(), p.FileSizeBytes)
			}
		},
		func(ev cachewatcher.ChangeEvent) {
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
