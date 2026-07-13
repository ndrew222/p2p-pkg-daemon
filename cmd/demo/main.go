// Command demo runs a live peer-to-peer transfer in one process: a seeder
// serves a package, a downloader fetches it over a real TCP connection, and
// the bytes are verified against their CID
// Run with:  go run ./cmd/demo
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/peer"
)

type memStore map[string][]byte

func (m memStore) Get(cid string) ([]byte, bool) { b, ok := m[cid]; return b, ok }

func main() {
	// 1. Make a "package" and compute its CID (SHA-256 hex)
	pkg := []byte("pretend this is a real .tgz package payload")
	sum := sha256.Sum256(pkg)
	cid := hex.EncodeToString(sum[:])
	log.Printf("package CID: %s", cid)

	// 2. Start a seeder holding that package (upload side, UC-06)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	seeder := &peer.Server{Store: memStore{cid: pkg}}
	go seeder.Serve(ln)
	time.Sleep(100 * time.Millisecond)

	// 3. Download from the seeder and verify (fetch side, UC-04)
	got, err := peer.FetchFromPeer(ln.Addr().String(), cid)
	if err != nil {
		log.Fatalf("fetch failed: %v", err)
	}
	log.Printf("downloaded and verified %d bytes: %q", len(got), got)
	log.Println("peer-to-peer transfer succeeded")
}
