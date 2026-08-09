// Command demo runs a live peer-to-peer transfer in one process over the real
// v0.2 peer wire: a seeder serves a package out of a stand-in pkg cache, a
// requester fetches it over a real TCP connection, and the bytes are verified
// against the expected hash and exact size from what would be pkg's repository
// database.
//
// It exists because the peer wire is otherwise only exercised by tests. Run
// with:  go run ./cmd/demo
//
// Everything here is production code except the cache and the expectation: the
// seeder is peer.Server on an http.Server, the source is daemon.CacheSource,
// and the requester is peer.FetchFromPeer. Nothing holds the package in
// memory at either end.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/ndrew222/p2p-pkg-daemon/internal/daemon"
	"github.com/ndrew222/p2p-pkg-daemon/internal/peer"
)

func main() {
	log.SetFlags(0)

	// 1. A stand-in pkg cache holding one "package". On a real host this is
	//    /var/cache/pkg and it is read-only to the daemon; here it is a
	//    throwaway directory so the demo needs no FreeBSD and no pkg.
	cacheDir, err := os.MkdirTemp("", "jmj-demo-cache-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(cacheDir)

	const nameVersion = "nginx-1.24.0_2"
	content := []byte("pretend this is a real .pkg archive payload")
	if err := os.WriteFile(filepath.Join(cacheDir, nameVersion+".pkg"), content, 0o644); err != nil {
		log.Fatal(err)
	}

	// 2. What pkg's repository database would say about it: the exact
	//    SHA-256 and the exact size, from the same row. Together they are
	//    the transfer bound -- there is no global size cap on this wire.
	sum := sha256.Sum256(content)
	want := peer.Want{Hash: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	log.Printf("package %s: %d bytes, sha256 %s", nameVersion, want.Size, want.Hash)

	// 3. The seeding side (UC-06): an ordinary HTTP server serving
	//    GET /pkg/<name-version> from open file handles. Both ADR-002 caps
	//    are left at 0, which is unlimited and is the default.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	seeder := &peer.Server{Source: daemon.NewCacheSource(cacheDir)}
	go func() {
		if err := seeder.Serve(ln); err != nil {
			log.Printf("seeder stopped: %v", err)
		}
	}()
	defer seeder.Close()

	// 4. The fetch side (UC-02): stream to a temp file, hash incrementally,
	//    verify against want. The result is an open file, never a []byte.
	tmp, err := peer.FetchFromPeer(context.Background(), ln.Addr().String(), nameVersion, want, os.TempDir())
	if err != nil {
		log.Fatalf("fetch failed: %v", err)
	}
	// The spool is per-request and ephemeral: the daemon has no store.
	defer peer.Discard(tmp)

	got, err := io.ReadAll(tmp) // safe here only because the demo payload is tiny
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("downloaded and verified %d bytes into %s: %q", len(got), tmp.Name(), got)

	// 5. And the failure the wire is built to make cheap: a package this
	//    seeder does not hold is a 404, and the requester moves on.
	if _, err := peer.FetchFromPeer(context.Background(), ln.Addr().String(), "notheld-1.0", want, os.TempDir()); err == nil {
		log.Fatal("a package the seeder does not hold should not have fetched")
	} else {
		log.Printf("not-held package correctly refused: %v", err)
	}

	log.Println("peer-to-peer transfer succeeded over the v0.2 HTTP wire")
}
