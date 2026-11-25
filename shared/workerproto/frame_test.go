package workerproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type shortReader struct{ io.Reader }

func (r shortReader) Read(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return r.Reader.Read(p)
}

type shortWriter struct{ io.Writer }

func (w shortWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Writer.Write(p)
}

type stalledWriter struct{}

func (stalledWriter) Write([]byte) (int, error) { return 0, nil }

func TestFramePartialIO(t *testing.T) {
	want := Envelope{Kind: Request, ID: 7, Op: Health, Payload: json.RawMessage("{}")}
	var buf bytes.Buffer
	if err := WriteEnvelope(shortWriter{&buf}, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEnvelope(shortReader{&buf})
	if err != nil || got.Kind != want.Kind || got.ID != want.ID ||
		got.Op != want.Op || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := ReadEnvelope(&buf); err != io.EOF {
		t.Fatalf("expected clean EOF, got %v", err)
	}
	if err := WriteEnvelope(stalledWriter{}, want); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected short-write error, got %v", err)
	}
}

func TestFrameBoundsAndTruncation(t *testing.T) {
	for _, n := range []uint32{0, MaxControlBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], n)
		if _, err := ReadEnvelope(bytes.NewReader(header[:])); !errors.Is(err, ErrFrameSize) {
			t.Fatalf("size %d: got %v", n, err)
		}
	}
	for _, p := range [][]byte{{0, 0, 0}, {0, 0, 0, 3, '{'}} {
		if _, err := ReadEnvelope(bytes.NewReader(p)); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected truncated frame, got %v", err)
		}
	}
	if err := writeFrame(io.Discard, nil); !errors.Is(err, ErrFrameSize) {
		t.Fatalf("empty frame: %v", err)
	}
}

func TestRejectMalformedControlMessages(t *testing.T) {
	bad := []string{
		"null", "[]", "{", "{}",
		`{"kind":"request","id":0,"op":"worker.health"}`,
		`{"kind":"request","id":1,"op":"worker.health","unexpected":true}`,
		`{"Kind":"request","id":1,"op":"worker.health"}`,
		`{"kind":"request","id":1,"id":2,"op":"worker.health"}`,
		`{"kind":"request","id":1,"op":"worker.health","payload":{"a":{"x":1,"x":2}}}`,
		`{"kind":"request","id":1,"op":"worker.health","payload":null}`,
		`{"kind":"request","id":1,"op":"worker.health","payload":[]}`,
		`{"kind":"request","id":1,"op":"worker.health","error":{"code":"bad"}}`,
		`{"kind":"request","id":1,"op":"worker.health"} {}`,
		`{"kind":"response","id":1,"payload":{},"error":{"code":"bad"}}`,
		`{"kind":"response","id":1,"error":{"code":""}}`,
		`{"kind":"response","id":1,"error":{"Code":"bad"}}`,
		`{"kind":"response","id":1,"op":"worker.health"}`,
		`{"kind":"cancel","id":1,"payload":{}}`,
		`{"kind":"event","id":1,"op":"worker.progress"}`,
		`{"kind":"unknown","id":1}`,
		`{"kind":"request","id":1,"op":"worker.health","payload":{"v":` +
			strings.Repeat("[", maxJSONDepth+1) + "0" + strings.Repeat("]", maxJSONDepth+1) + "}}",
	}
	for _, body := range bad {
		var buf bytes.Buffer
		if err := writeFrame(&buf, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadEnvelope(&buf); !errors.Is(err, ErrMessage) {
			t.Errorf("accepted invalid message %q: %v", body, err)
		}
	}
	var buf bytes.Buffer
	if err := writeFrame(&buf, []byte{0x7b, 0xff, 0x7d}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnvelope(&buf); !errors.Is(err, ErrMessage) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
}

func TestHandshakeRefusesWrongPeer(t *testing.T) {
	for _, body := range []string{
		`{"version":2,"role":"worker"}`,
		`{"version":1,"role":"library"}`,
		`{"version":1,"role":"worker","role":"library"}`,
		`{"version":1,"role":"worker","extra":true}`,
	} {
		var in bytes.Buffer
		if err := writeFrame(&in, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := Handshake(&in, io.Discard, Library); !errors.Is(err, ErrHandshake) {
			t.Errorf("expected handshake refusal for %s: %v", body, err)
		}
	}
	if err := Handshake(bytes.NewReader(nil), io.Discard, Role("external")); !errors.Is(err, ErrHandshake) {
		t.Fatalf("invalid local role: %v", err)
	}
}
