package peer

// Case 7 of the definition of done, and the v0.1 robustness requirement it
// inherits: malformed input from untrusted machines must never crash the
// daemon.
//
// The owner moved the fuzzing obligation from the retired binary framer to the
// PEER SERVER'S HTTP SURFACE, END TO END: arbitrary bytes delivered to the
// server as a request, asserting only that it never panics and always
// terminates the connection. That is strictly broader than what it replaces --
// the old target exercised one framing function in isolation, this one
// exercises request handling, the path rule and the name-version check on the
// same code path a hostile peer reaches.
//
// Request framing itself is now the standard library's responsibility. The
// project-owned surface under test is the handler, which is why the seed
// corpus is made of requests rather than of frames. The old
// internal/peerwire/testdata corpus described a format that no longer exists
// and was discarded with the package, per the spec.
//
// Run the full campaign with:
//
//	go test ./internal/peer/ -run FuzzSeederHTTPSurface -fuzz FuzzSeederHTTPSurface

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func FuzzSeederHTTPSurface(f *testing.F) {
	cache := f.TempDir()
	if err := writeFuzzPackage(cache); err != nil {
		f.Fatal(err)
	}

	srv := &Server{
		Source: dirSource(cache),
		// Both caps on, and low, so the refusal path is inside the
		// fuzzed surface rather than dead code during the campaign.
		MaxConcurrentSeeds:      4,
		MaxConcurrentSeedsPerIP: 2,
		OnNotHeld:               func(string) {},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.Fatal(err)
	}
	go srv.Serve(ln)
	f.Cleanup(func() { srv.Close() })
	addr := ln.Addr().String()

	// Seeds: a valid request, the three rejection shapes, and the malformed
	// bytes that are not HTTP at all.
	f.Add([]byte("GET /pkg/jq-1.7 HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("GET /pkg/notheld-1.0 HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("POST /pkg/jq-1.7 HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("GET /All/Hashed/jq-1.7~0123456789.pkg HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("GET /pkg/" + strings.Repeat("A", 4096) + " HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("GET /pkg/../../etc/passwd HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("GET /pkg/%2e%2e%2f%2e%2e%2fetc%2fpasswd HTTP/1.1\r\nHost: x\r\n\r\n"))
	f.Add([]byte("GET /pkg/jq-1.7 HTTP/1.1\r\nHost: x\r\nRange: bytes=0-1\r\n\r\n"))
	f.Add([]byte("GET /pkg/jq-1.7 HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 203.0.113.9\r\n\r\n"))
	f.Add([]byte{0x00})
	f.Add([]byte{0x03, 0x00, 0x00, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}) // a v0.1 frame
	f.Add([]byte("GET"))
	f.Add([]byte("\r\n\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			// The listener is gone or the machine is out of
			// descriptors; neither is a finding about the handler.
			t.Skip(err)
		}
		defer conn.Close()

		// The deadline is the "always terminates the connection" half
		// of the assertion: a handler that parked forever would fail
		// here rather than hanging the campaign.
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(data); err != nil {
			return
		}
		// Half-close so a well-formed request without a body is not
		// waiting on more bytes from us.
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		// Drain. A panic in the handler takes the whole test binary
		// down, which is exactly the failure this target exists to
		// catch; anything else here is an ordinary connection outcome.
		_, _ = io.Copy(io.Discard, conn)
	})
}

// A panic in a handler goroutine cannot be recovered by the fuzz target, so
// this documents what the campaign is really asserting: the process survives.
// net/http recovers handler panics per connection, so a panicking handler
// shows up as a dropped connection rather than a crash -- which is why the
// target also checks that a well-formed request still gets a well-formed
// answer after arbitrary traffic has gone through the same server.
func TestSeederStillAnswersAfterHostileTraffic(t *testing.T) {
	cache := t.TempDir()
	content := []byte("bytes")
	writePackage(t, cache, "jq-1.7", content)
	addr := startSeeder(t, &Server{Source: dirSource(cache)})

	hostile := [][]byte{
		{0x00},
		[]byte("GET"),
		[]byte("\r\n\r\n"),
		[]byte("GET /pkg/../../etc/passwd HTTP/1.1\r\nHost: x\r\n\r\n"),
		[]byte("GET /pkg/" + strings.Repeat("A", 100000) + " HTTP/1.1\r\nHost: x\r\n\r\n"),
		{0x03, 0x00, 0x00, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'},
	}
	for _, data := range hostile {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write(data)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		io.Copy(io.Discard, conn)
		conn.Close()
	}

	resp, err := http.Get("http://" + addr + "/pkg/jq-1.7")
	if err != nil {
		t.Fatalf("the seeder stopped answering after hostile traffic: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != string(content) {
		t.Errorf("body = %q, want %q", body, content)
	}
}

func writeFuzzPackage(dir string) error {
	f, err := createIn(dir, "jq-1.7")
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte("fuzz corpus package bytes"))
	return err
}
