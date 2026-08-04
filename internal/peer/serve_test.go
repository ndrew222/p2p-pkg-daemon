package peer

import (
	"net"
	"testing"
	"time"
)

// memSource stands in for the read-only pkg cache.
type memSource map[string][]byte

func (m memSource) Get(nameVersion string) ([]byte, bool) { b, ok := m[nameVersion]; return b, ok }

func TestServeAndFetchEndToEnd(t *testing.T) {
	content := []byte("real end to end package")
	name := "jq-1.7"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &Server{Source: memSource{name: content}}
	go srv.Serve(ln)

	got, err := FetchFromPeer(ln.Addr().String(), name, hashOf(content))
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}

	// A package the peer does not hold -> peer returns an error, fetch surfaces it.
	if _, err := FetchFromPeer(ln.Addr().String(), "notheld-1.0", hashOf(content)); err == nil {
		t.Fatal("expected error for a package the peer does not hold")
	}
}

// Serve used to log-and-continue on every Accept error, so closing the
// listener span the loop at full tilt instead of ending it. Shutdown must
// return.
func TestServeReturnsOnClosedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Source: memSource{}}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	ln.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil on a closed listener, want the accept error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the listener was closed")
	}
}
