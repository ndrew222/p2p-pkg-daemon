package peer

import (
	"log"
	"net"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/peerwire"
)

// Store is anything that can look up package bytes by CID
type Store interface {
	Get(cid string) ([]byte, bool)
}

// Server is the part of the daemon that sends packages to other peers (UC-06).
// Requests come from untrusted machines, so if one sends broken data we just
// close that connection instead of crashing
type Server struct {
	Store Store
}

func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts connections on an existing listener
func (s *Server) Serve(ln net.Listener) error {
	log.Printf("peer: seed server listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("peer: accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	msg, err := peerwire.ReadMessage(conn)
	if err != nil {
		return // malformed / truncated input -> drop quietly, never crash
	}
	if msg.Type != peerwire.MsgRequest {
		writeError(conn, "expected REQUEST")
		return
	}

	cid := string(msg.Payload)
	if !validCID(cid) {
		writeError(conn, "malformed cid")
		return
	}

	content, ok := s.Store.Get(cid)
	if !ok {
		writeError(conn, "404: not held") // cache may have been cleared since announce
		return
	}
	_, _ = conn.Write(peerwire.Encode(peerwire.Message{Type: peerwire.MsgData, Payload: content}))
	log.Printf("peer: served cid=%q (%d bytes)", cid, len(content))
}

func writeError(conn net.Conn, msg string) {
	_, _ = conn.Write(peerwire.Encode(peerwire.Message{Type: peerwire.MsgError, Payload: []byte(msg)}))
}
