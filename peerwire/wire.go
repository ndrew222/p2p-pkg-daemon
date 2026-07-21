// Package peerwire is the binary framing for the peer-to-peer data channel.
// NOTE: ADR-001 pins peer transport to HTTP-over-TCP; this binary format is
// interim until the v0.2 wire spec lands. The identifier carried in a REQUEST
// is now a name-version string (not a CID).
package peerwire

import (
	"encoding/binary"
	"errors"
	"io"
)

type Message struct {
	Type    byte
	Payload []byte
}

const (
	MsgRequest byte = 3 // daemon -> peer:   "give me this name-version"
	MsgData    byte = 4 // peer   -> daemon: the package bytes
	MsgError   byte = 5 // peer   -> daemon: a short error string
)

const MaxPayload = 64 << 20 // 64 MiB

var ErrBadFrame = errors.New("peerwire: malformed frame")

func Encode(m Message) []byte {
	out := make([]byte, 5+len(m.Payload))
	out[0] = m.Type
	binary.BigEndian.PutUint32(out[1:5], uint32(len(m.Payload)))
	copy(out[5:], m.Payload)
	return out
}

func Parse(data []byte) (Message, error) {
	if len(data) < 5 {
		return Message{}, ErrBadFrame
	}
	length := binary.BigEndian.Uint32(data[1:5])
	if len(data) < 5+int(length) {
		return Message{}, ErrBadFrame
	}
	return Message{Type: data[0], Payload: data[5 : 5+length]}, nil
}

func ReadMessage(r io.Reader) (Message, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return Message{}, err
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length > MaxPayload {
		return Message{}, ErrBadFrame
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Message{}, ErrBadFrame
	}
	return Message{Type: header[0], Payload: payload}, nil
}
