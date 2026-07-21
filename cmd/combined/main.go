// Command combined runs the whole system in one process: tracker +
// discovery, and peer serve/fetch.  To run: ./cmd/combined
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/discovery"
	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
	"github.com/ndrew222/p2p-pkg-daemon/internal/tracker"
	"github.com/ndrew222/p2p-pkg-daemon/peer"
)

type memStore map[string][]byte

func (m memStore) Get(cid string) ([]byte, bool) { b, ok := m[cid]; return b, ok }

// lister adapts discovery. Client (returns []proto.PeerInfo) to
// peer.PeerLister (returns []string). This glues the two halves
type lister struct{ c *discovery.Client }

func (l lister) Peers(cid string) ([]string, error) {
	infos, err := l.c.Peers(cid)
	if err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(infos))
	for _, p := range infos {
		addrs = append(addrs, p.Addr)
	}
	return addrs, nil
}

// startTracker runs tracker behind the HTTP endpoints his client calls
func startTracker(addr string) {
	t := tracker.New()
	go t.RunSweeper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /announce", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req proto.AnnounceRequest
		if proto.Decode(body, &req) != nil || req.Validate() != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		t.Announce(&req)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /ping", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req proto.PingRequest
		if proto.Decode(body, &req) != nil || req.Validate() != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if !t.Ping(&req) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, r *http.Request) {
		cid := r.URL.Query().Get("cid")
		if proto.ValidateCID(cid) != nil {
			http.Error(w, "bad cid", http.StatusBadRequest)
			return
		}
		peers := t.Peers(cid)
		if peers == nil {
			peers = []proto.PeerInfo{}
		}
		b, _ := proto.Encode(proto.PeerListResponse{CID: cid, Peers: peers})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	go http.ListenAndServe(addr, mux)
}

func main() {
	startTracker("127.0.0.1:8080")
	trackerURL := "http://127.0.0.1:8080"
	time.Sleep(150 * time.Millisecond)
	log.Println("=== tracker up ===")

	pkg := []byte("pretend this is a real .tgz package payload")
	sum := sha256.Sum256(pkg)
	cid := hex.EncodeToString(sum[:])

	// Daemon A: your seeder. Serves the bytes AND announces to the tracker.
	lnA, _ := net.Listen("tcp", "127.0.0.1:0")
	seedAddr := lnA.Addr().String()
	go (&peer.Server{Store: memStore{cid: pkg}}).Serve(lnA)
	clientA := discovery.New(trackerURL, proto.PeerID("peerA"), seedAddr)
	if err := clientA.Announce([]string{cid}); err != nil {
		log.Fatalf("announce: %v", err)
	}
	log.Printf("=== daemon A seeding on %s, announced ===", seedAddr)

	// Daemon B: your downloader. Discovers via tracker, fetches, verifies.
	clientB := discovery.New(trackerURL, proto.PeerID("peerB"), "127.0.0.1:9999")
	data, err := peer.Download(lister{c: clientB}, cid)
	if err != nil {
		log.Fatalf("download: %v", err)
	}
	log.Printf("=== daemon B downloaded and verified %d bytes: %q ===", len(data), data)
	log.Println("FULL FLOW SUCCEEDED: announce -> discover -> fetch -> verify")
}
