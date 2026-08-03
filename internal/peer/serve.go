package peer

import (
	"errors"
	"log"
	"net"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/peerwire"
)

// PackageSource is the read-only view of pkg's cache the serving side reads
// from. Per spec the daemon has NO store of its own -- it serves bytes
// straight from the pkg cache (read-only). Any type that returns the bytes
// for a name-version satisfies this.
type PackageSource interface {
	Get(nameVersion string) ([]byte, bool)
}

// Server is the serving/seeding side (UC-06). Incoming requests are untrusted
// network input, so each is validated and a malformed request drops the
// connection rather than crashing (the fuzz target). No hashing happens here,
// the requesting daemon verifies on its own end
type Server struct {
	Source PackageSource
}

func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// acceptRetryDelay bounds the back-off after a temporary Accept failure
// (typically EMFILE): long enough that the loop is not a spin, short enough
// that seeding resumes as soon as descriptors free up. Same shape as
// net/http.Server.Serve.
const (
	acceptRetryDelayMin = 5 * time.Millisecond
	acceptRetryDelayMax = time.Second
)

func (s *Server) Serve(ln net.Listener) error {
	log.Printf("peer: seed server listening on %s", ln.Addr())
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			// A permanent error -- above all a closed listener, which is how
			// shutdown reaches this loop -- must end Serve. Previously every
			// error was logged and retried, so a closed listener span the loop
			// at full tilt, burning a core and flooding the log.
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				log.Printf("peer: accept: %v; seed server stopping", err)
				return err
			}
			// Temporary: back off and keep serving.
			if delay == 0 {
				delay = acceptRetryDelayMin
			} else if delay *= 2; delay > acceptRetryDelayMax {
				delay = acceptRetryDelayMax
			}
			log.Printf("peer: accept: %v; retrying in %v", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	msg, err := peerwire.ReadMessage(conn)
	if err != nil {
		return // malformed / truncated: drop quietly, never crash
	}
	if msg.Type != peerwire.MsgRequest {
		writeError(conn, "expected REQUEST")
		return
	}

	nameVersion := string(msg.Payload)
	if !validName(nameVersion) {
		writeError(conn, "invalid name-version")
		return
	}

	content, ok := s.Source.Get(nameVersion)
	if !ok {
		writeError(conn, "404: not held") // pkg clean may have removed it since announce
		return
	}
	_, _ = conn.Write(peerwire.Encode(peerwire.Message{Type: peerwire.MsgData, Payload: content}))
	log.Printf("peer: served %q (%d bytes)", nameVersion, len(content))
}

func writeError(conn net.Conn, msg string) {
	_, _ = conn.Write(peerwire.Encode(peerwire.Message{Type: peerwire.MsgError, Payload: []byte(msg)}))
}
