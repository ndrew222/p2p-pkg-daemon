package peerwire

import (
	"encoding/binary"
	"errors"
	"io"
)

// Message is one framed message: [type][4-byte length][payload].
type Message struct {
	Type    byte
	Payload []byte
}

const (
	MsgRequest byte = 3 // daemon -> peer:   "give me this CID"
	MsgData    byte = 4 // peer   -> daemon: the package bytes
	MsgError   byte = 5 // peer   -> daemon: a short error string
)

// MaxPayload caps how many bytes we allocate for one message, so a hostile
// length field can never make us allocate unbounded memory
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
	// guard line
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
	// guardline
	if length > MaxPayload {
		return Message{}, ErrBadFrame
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Message{}, ErrBadFrame
	}
	return Message{Type: header[0], Payload: payload}, nil
}
