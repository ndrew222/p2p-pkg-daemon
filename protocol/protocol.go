package protocol

import (
	"encoding/binary"
	"errors"
)

// message to send over the network (a type byte + some bytes)
// type byte to label message, some bytes for content/details of message
type Message struct {
	Type    byte
	Payload []byte
}

// Encode turns a message into bytes to send: [type][4-byte length][payload]
func Encode(m Message) []byte {
	out := make([]byte, 5+len(m.Payload)) // size is 1 type byte + 4 length bytes + payload size
	out[0] = m.Type
	binary.BigEndian.PutUint32(out[1:5], uint32(len(m.Payload)))
	copy(out[5:], m.Payload)
	return out
}

var ErrBadFrame = errors.New("protocol: malformed frame")

// Parse turns received bytes back into a message, rejecting any bad input
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
