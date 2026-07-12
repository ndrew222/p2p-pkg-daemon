package protocol

import "testing"

// FuzzParse throws random bytes at Parse and fails only if Parse crashes
func FuzzParse(f *testing.F) {
	// one valid, two broken tests
	f.Add(Encode(Message{Type: 1, Payload: []byte("hello")}))
	f.Add([]byte{0x01})                         // too short
	f.Add([]byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF}) // claim huge length

	// Run Parse against endless random mutations of seeds above
	f.Fuzz(func(t *testing.T, data []byte) {
		Parse(data)
	})
}
