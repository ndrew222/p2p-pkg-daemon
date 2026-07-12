package protocol

import (
	"encoding/binary"
	"io"
)

const (
	MsgIWant   byte = 1 // daemon -> tracker: "who has this?"
	MsgPeers   byte = 2 // tracker - > daemon: list of peers
	MsgRequest byte = 3 // daemon -> peer: "give me this package"
	MsgData    byte = 4 // peer -> daemon : the package bytes
)

// Caps how many bytes we will ever alloc for one message
const MaxPayload = 64 << 20 // 64 MiB

// reads exactly one message from a live connection (handle one by one)
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
		return Message{}, err
	}
	return Message{Type: header[0], Payload: payload}, nil
}
