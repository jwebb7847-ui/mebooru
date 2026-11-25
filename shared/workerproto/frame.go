package workerproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var (
	ErrFrameSize = errors.New("invalid control frame size")
	ErrMessage = errors.New("invalid control message")
	ErrHandshake = errors.New("incompatible private worker handshake")
)

const maxJSONDepth = 64

// readFrame rejects oversized lengths before allocating. The caller must
// close the connection on malformed frames and on I/O failure; do not try
// to resynchronize a damaged stream.
func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxControlBytes {
		return nil, ErrFrameSize
	}
	body := make([]byte, int(n))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// writeFrame requires a single writer per endpoint. A future session
// implementation owns serialization and closes the channel on write errors.
func writeFrame(w io.Writer, body []byte) error {
	if len(body) == 0 || len(body) > MaxControlBytes {
		return ErrFrameSize
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, body)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Reject duplicate keys at every depth, including in operation payloads.
// The standard JSON unmarshaller otherwise accepts the last occurrence.
// UseNumber avoids conversion to floating point while validating syntax.
func scanValue(d *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return ErrMessage
	}
	tok, err := d.Token()
	if err != nil {
		return ErrMessage
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return ErrMessage
			}
			seen[name] = true
			if err := scanValue(d, depth+1); err != nil {
				return err
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return ErrMessage
		}
	case '[':
		for d.More() {
			if err := scanValue(d, depth+1); err != nil {
				return err
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return ErrMessage
		}
	default:
		return ErrMessage
	}
	return nil
}

// decodeObject requires canonical field names (Go's default matching is
// case insensitive), no explicit null fields, no trailing values and a
// bounded nesting depth. Operation-specific payload validation comes later.
func decodeObject(body []byte, dst any, allowed ...string) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(body) {
		return ErrMessage
	}
	scanner := json.NewDecoder(bytes.NewReader(body))
	scanner.UseNumber()
	if err := scanValue(scanner, 0); err != nil {
		return err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return ErrMessage
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return ErrMessage
	}
	for key, value := range fields {
		valid := false
		for _, name := range allowed {
			if key == name {
				valid = true
				break
			}
		}
		if !valid || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrMessage
		}
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return ErrMessage
	}
	return nil
}

func validateEnvelope(e Envelope) error {
	hasPayload := len(e.Payload) != 0
	if hasPayload {
		p := bytes.TrimSpace(e.Payload)
		if len(p) == 0 || p[0] != '{' {
			return ErrMessage
		}
	}
	switch e.Kind {
	case Request:
		if e.ID == 0 || e.Op == "" || e.Error != nil {
			return ErrMessage
		}
	case Response:
		if e.ID == 0 || e.Op != "" {
			return ErrMessage
		}
		if e.Error != nil && (hasPayload || strings.TrimSpace(e.Error.Code) == "") {
			return ErrMessage
		}
	case Cancel:
		if e.ID == 0 || e.Op != "" || hasPayload || e.Error != nil {
			return ErrMessage
		}
	case Event:
		if e.ID != 0 || e.Op == "" || e.Error != nil {
			return ErrMessage
		}
	default:
		return ErrMessage
	}
	return nil
}

func decodeEnvelope(body []byte) (Envelope, error) {
	var e Envelope
	if err := decodeObject(body, &e, "kind", "id", "op", "payload", "error"); err != nil {
		return Envelope{}, err
	}
	if e.Error != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return Envelope{}, ErrMessage
		}
		var fault Fault
		if err := decodeObject(fields["error"], &fault, "code", "message"); err != nil {
			return Envelope{}, err
		}
	}
	if err := validateEnvelope(e); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// ReadEnvelope validates structure only. A dispatcher must still check
// direction, operation allowlist, session state and operation-specific fields.
func ReadEnvelope(r io.Reader) (Envelope, error) {
	body, err := readFrame(r)
	if err != nil {
		return Envelope{}, err
	}
	return decodeEnvelope(body)
}

// WriteEnvelope uses the same structural checks as the receiver.
func WriteEnvelope(w io.Writer, e Envelope) error {
	body, err := json.Marshal(e)
	if err != nil {
		return ErrMessage
	}
	if len(body) > MaxControlBytes {
		return ErrFrameSize
	}
	if _, err := decodeEnvelope(body); err != nil {
		return err
	}
	return writeFrame(w, body)
}

func sendHello(w io.Writer, self Role) error {
	body, err := json.Marshal(Hello{Version: Version, Role: self})
	if err != nil {
		return ErrHandshake
	}
	return writeFrame(w, body)
}

func receiveHello(r io.Reader, expected Role) error {
	body, err := readFrame(r)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	var hello Hello
	if err := decodeObject(body, &hello, "version", "role"); err != nil {
		return ErrHandshake
	}
	if hello.Version != Version || hello.Role != expected {
		return ErrHandshake
	}
	return nil
}

// Handshake exchanges exact version and role before ordinary messages.
// Library sends first, worker reads first: this avoids pipe-buffer deadlock.
// It does not authenticate an arbitrary executable. The supervisor owns
// child identity, time limits, pipe closure and failure handling.
func Handshake(r io.Reader, w io.Writer, self Role) error {
	switch self {
	case Library:
		if err := sendHello(w, self); err != nil {
			return err
		}
		return receiveHello(r, Worker)
	case Worker:
		if err := receiveHello(r, Library); err != nil {
			return err
		}
		return sendHello(w, self)
	default:
		return ErrHandshake
	}
}
