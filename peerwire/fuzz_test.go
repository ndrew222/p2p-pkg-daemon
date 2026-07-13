package peerwire

import (
	"bytes"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add(Encode(Message{Type: MsgData, Payload: []byte("hello")}))
	f.Add([]byte{0x03})
	f.Add([]byte{0x04, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		Parse(data)
	})
}

func FuzzReadMessage(f *testing.F) {
	f.Add(Encode(Message{Type: MsgData, Payload: []byte("hello")}))
	f.Add([]byte{0x03})
	f.Add([]byte{0x04, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		ReadMessage(bytes.NewReader(data))
	})
}
