package peer

import (
	"net"
	"testing"
)

type memStore map[string][]byte

func (m memStore) Get(cid string) ([]byte, bool) { b, ok := m[cid]; return b, ok }

func TestServeAndFetchEndToEnd(t *testing.T) {
	content := []byte("real end to end package")
	cid := cidOf(content)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &Server{Store: memStore{cid: content}}
	go srv.Serve(ln)

	got, err := FetchFromPeer(ln.Addr().String(), cid)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}

	if _, err := FetchFromPeer(ln.Addr().String(), cidOf([]byte("not stored"))); err == nil {
		t.Fatal("expected error for a CID the peer does not hold")
	}
}
