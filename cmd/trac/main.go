package main

import (
	"encoding/json" // encode peer list out of wire
	"flag"          // command line arg parsing. needed to run ./trac -addr :9000
	"io"            // input/output primitives. Want io.ReadAll and io.LimitReader
	"log"           // timestamped output to stderr
	"net/http"      // HTTP server

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"   // own package
	"github.com/ndrew222/p2p-pkg-daemon/internal/tracker" // own package
)

// maxBody caps how much we will read from any request
// Mirrors the cap inside proto.Decode; enforced here so a hostile peer cannot make us read gigabytes before we even try to parse
// must be written before any read calls
const maxBody = 1 << 20 // 1 MiB

func main() {
	// returns *string cause the command line hasnt been parsed at this momemnt, so the pointer slot will be filled later
	addr := flag.String("addr", ":8080", "listen address")
	// reads os.Args and fills those slots
	flag.Parse()

	// returns *tracker.Tracker with both maps make-d
	// contstruct the registry, holds all the trackers state and lives the entire life of the process
	t := tracker.New()
	// starts a goroutine, run this function concurrently
	// need go to prevent it from blocking forever, it runs in the bg while main proceeds
	// from this line on, sweeper goroutine(15s schedule) and HTTP handlers(whenever a request lands) touch the maps
	go t.RunSweeper()

	mux := http.NewServeMux()                           // lookup table: given incoming request method and path, which func handles it?
	mux.HandleFunc("POST /announce", handleAnnounce(t)) // method and path together
	mux.HandleFunc("POST /ping", handlePing(t))
	mux.HandleFunc("GET /peers", handlePeers(t))

	log.Printf("tracker: listening on %s", *addr)           // prints dereferenced pointer to know server started and on which port
	if err := http.ListenAndServe(*addr, mux); err != nil { //opens TCP socket on *addr, listens for connections and look up incoming request in mux and calls matching handler. err!=nil is crash path if server fails (port alr in use or socket error)
		log.Fatalf("tracker: server died: %v", err)
	}
}

// readBody reads at most maxBody bytes (1 Mib) from the request (instead of say 4GB)
// io.ReadAll drains stream into []byte, reads until end of input
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxBody))
}

func handleAnnounce(t *tracker.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		// read the body, bounded
		body, err := readBody(r)
		if err != nil {
			// writes status code and meesage as the response body (setting header and writting the text yourself)
			// http.StatusBadRequest: known constant for 400, typing 400 directly tells reader nothing
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}

		var req proto.AnnounceRequest
		if err := proto.Decode(body, &req); err != nil {
			log.Printf("tracker: bad announce: %v", err)
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}

		if err := req.Validate(); err != nil {
			log.Printf("tracker: invalid announce: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		t.Announce(&req)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handlePing(t *tracker.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}

		var req proto.PingRequest
		if err := proto.Decode(body, &req); err != nil {
			log.Printf("tracker: bad ping: %v", err)
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}

		if err := req.Validate(); err != nil {
			log.Printf("tracker: invalid ping: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		if !t.Ping(&req) {
			// Unknown peer. Tell it to announce first
			// happens when tracker restarted and every peer is forgotten but daemons dont know that
			// this allows system to heal itself
			http.Error(w, "unknown peer; announce first", http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func handlePeers(t *tracker.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// r.URl.Query() parse query string into map structure
		// .Get("cid") pulls out that key
		// if key is absent, .Get returns "", no error/panic as missing and empty are indistinguishable here as both are invlaid and rejected by next line
		cid := r.URL.Query().Get("cid")

		// validating CID string from network that would be used as map key
		// if incoming CID string is empty, ?cid= would loop up t.cids[""] and ?cid=<10kb of junk> would allocate 10kb map key
		if err := proto.ValidateCID(cid); err != nil {
			log.Printf("tracker: bad peers query: %v", err)
			http.Error(w, "malformed cid", http.StatusBadRequest)
			return
		}

		peers := t.Peers(cid)
		if peers == nil {
			peers = []proto.PeerInfo{} // encode as [], not null. Go allows range/len over empty slice, but other language can throw. Setting to null instead of nil slice resolves that
		}

		resp := proto.PeerListResponse{CID: cid, Peers: peers}

		w.Header().Set("Content-Type", "application/json")      // tell client what its getting, otherwise Go will sniff first bytes and say text/plain. Must be sent on the wire before body, sending one after silently does nothing
		if err := json.NewEncoder(w).Encode(resp); err != nil { // encoder that writes directly to ResponseWriter, streaming as it goes. error only logged, not sent to client.By the time encode fails, you would have already written the response (headers sent and some body sent), cant go back.
			log.Printf("tracker: write response: %v", err)
		}
	}
}
