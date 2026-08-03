// Command trac is the tracker daemon: three HTTP endpoints over an in-memory
// registry, per docs/tracker-protocol-spec-v0.2.md.
//
// Every body and query parameter here is untrusted input from machines on the
// network. The tracker is a declared fuzz target: malformed input of any shape
// must produce an error status and leave the process serving.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
	"github.com/ndrew222/p2p-pkg-daemon/internal/tracker"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	t := tracker.New()
	// The sweeper blocks forever, so it gets its own goroutine. From here
	// on it and the handlers both touch the registry; the registry's mutex
	// is what makes that safe.
	go t.RunSweeper()

	log.Printf("tracker: listening on %s", *addr)
	if err := http.ListenAndServe(*addr, newMux(t)); err != nil {
		log.Fatalf("tracker: server died: %v", err)
	}
}

// newMux wires the three-path surface. Separate from main so the tests can
// drive the real routing rather than the handlers in isolation.
func newMux(t *tracker.Tracker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /announce", handleAnnounce(t))
	mux.HandleFunc("POST /ping", handlePing(t))
	mux.HandleFunc("GET /peers", handlePeers(t))
	return recoverPanics(mux)
}

// recoverPanics keeps one bad request from taking the process down. The spec
// makes this explicit: "A panic in a handler must not take the process down
// (recover per-request)."
//
// net/http already recovers panics per connection, but it kills the
// connection silently. Recovering here means the client gets a 500 and the
// panic is logged with the path that caused it.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("tracker: PANIC handling %s %s: %v", r.Method, r.URL.Path, v)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP is the peer's identity. §Wire encoding: "the daemon's IP is always
// the connection's source address. It is never read from a header or body.
// X-Forwarded-For and friends are ignored; the tracker is not run behind a
// trusted proxy in v0.x."
//
// Do not add a header fallback here. Any client could then claim any IP, and
// every entry in the registry is keyed on this value.
func clientIP(r *http.Request) (string, bool) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr is set by the server, not the client, so this
		// should not happen outside a test with a hand-built request.
		log.Printf("tracker: unparsable RemoteAddr %q: %v", r.RemoteAddr, err)
		return "", false
	}
	return ip, true
}

// writeJSON sends a status and a JSON body. The Content-Type must be set
// before WriteHeader; setting a header afterwards silently does nothing.
//
// An encode failure can only be logged. By the time it happens the status
// line and part of the body are already on the wire.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("tracker: write response: %v", err)
	}
}

// readBody bounds the request body before reading a byte of it.
// http.MaxBytesReader is what makes the cap real: io.LimitReader would
// silently truncate an oversized body into something that might still parse,
// which is worse than rejecting it.
//
// It reports whether the body was oversized, because the spec distinguishes
// the two failures: too big is 413, unparsable is 400.
func readBody(w http.ResponseWriter, r *http.Request) (body []byte, oversized bool, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, proto.MaxBodyBytes)

	body, err = io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		return nil, errors.As(err, &tooLarge), err
	}
	return body, false, nil
}

func handleAnnounce(t *tracker.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, ok := clientIP(r)
		if !ok {
			http.Error(w, "cannot determine source address", http.StatusBadRequest)
			return
		}

		body, oversized, err := readBody(w, r)
		if err != nil {
			if oversized {
				log.Printf("tracker: oversized announce from %s", ip)
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}

		var req proto.AnnounceRequest
		if err := proto.Decode(body, &req); err != nil {
			log.Printf("tracker: bad announce from %s: %v", ip, err)
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}

		if err := req.Validate(); err != nil {
			// An oversized package list is a resource-exhaustion
			// attempt, not a typo, and the spec gives it its own
			// status.
			if errors.Is(err, proto.ErrTooManyPackages) {
				log.Printf("tracker: oversized announce list from %s: %v", ip, err)
				http.Error(w, "too many packages", http.StatusRequestEntityTooLarge)
				return
			}
			log.Printf("tracker: invalid announce from %s: %v", ip, err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		// Accepted from any IP, known or unknown, solicited or not. An
		// empty list is the deregistration path and reaches the
		// tracker so the entry is deleted.
		t.Announce(ip, &req)
		writeJSON(w, http.StatusOK, proto.StatusResponse{Status: proto.StatusAck})
	}
}

func handlePing(t *tracker.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, ok := clientIP(r)
		if !ok {
			http.Error(w, "cannot determine source address", http.StatusBadRequest)
			return
		}

		// §POST /ping: "Bare keep-alive. No body (any body is
		// ignored)." Drain and discard so the connection can be
		// reused; do not parse, and do not reject a request for
		// carrying one.
		_, _, _ = readBody(w, r)

		if !t.Ping(ip) {
			// THIS 404 IS LOAD-BEARING. It is the protocol's
			// requestPackageList message: the daemon's correct
			// response is to POST /announce, not to retry the ping.
			// It is also the entire tracker-restart self-healing
			// path -- tracker forgets everything, next ping 404s,
			// daemon re-announces. Do not "fix" it into a 200.
			writeJSON(w, http.StatusNotFound, proto.StatusResponse{Status: proto.StatusUnknown})
			return
		}

		writeJSON(w, http.StatusOK, proto.StatusResponse{Status: proto.StatusAck})
	}
}

func handlePeers(t *tracker.Tracker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Query().Get returns "" for both an absent and an empty
		// parameter. The spec treats both as 400, so they do not need
		// telling apart.
		nameVersion := r.URL.Query().Get("pkg")
		if nameVersion == "" {
			http.Error(w, "missing pkg parameter", http.StatusBadRequest)
			return
		}

		// This string becomes a map key and reaches a log line, so it
		// is bounded and stripped of control characters before use.
		if err := proto.ValidateNameVersion(nameVersion); err != nil {
			log.Printf("tracker: bad peers query: %v", err)
			http.Error(w, "malformed pkg parameter", http.StatusBadRequest)
			return
		}

		// A miss is 200 with an empty array, not a 404. The only 404
		// in this protocol is the unknown-pinger signal above.
		writeJSON(w, http.StatusOK, proto.PeerListResponse{Peers: t.Peers(nameVersion)})
	}
}
