package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrTooLarge = errors.New("proto: payload exceeds maximum size")

// Deocde parses JSON bytes into dst, which must be a pointer to a struct
// checks for truncated JSON, malformed syntax and type mismatch

func Decode(data []byte, dst any) error {

	// rejects huge data. The tracker also caps the body before reading it
	// (http.MaxBytesReader); this is the second line of defence, for bytes
	// that arrived some other way.
	if len(data) > MaxBodyBytes {
		return ErrTooLarge
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("proto: decode: %w", err)
	}

	// Reject anything after the first JSON value: `{"a":1}{"b":2}` or `{} junk`
	if dec.More() {
		return errors.New("proto: unexpected trailing data")
	}

	return nil
}

// Encode serialises v to JSON bytes.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("proto: encode: %w", err)
	}
	return b, nil
}
