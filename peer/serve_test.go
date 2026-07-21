package peer

import (
	"net"
	"testing"
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
