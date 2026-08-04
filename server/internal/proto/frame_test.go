package proto

import (
	"bytes"
	"errors"
	"testing"
)

func TestCRC16CCITTFalse(t *testing.T) {
	if got := CRC16CCITTFalse([]byte("123456789")); got != 0x29b1 {
		t.Fatalf("CRC16CCITTFalse() = %#04x, want 0x29b1", got)
	}
}

func TestEncodeDecode(t *testing.T) {
	wire, err := Encode(Frame{Opcode: 5, Seq: 99, Payload: []byte{0, 1, 2, 0, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if wire[len(wire)-1] != 0 {
		t.Fatal("missing delimiter")
	}
	frame, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Opcode != 5 || frame.Seq != 99 || !bytes.Equal(frame.Payload, []byte{0, 1, 2, 0, 3}) {
		t.Fatalf("unexpected frame: %#v", frame)
	}
}

func TestDecodeRejectsBadCRC(t *testing.T) {
	wire, err := Encode(Frame{Opcode: 1, Seq: 2, Payload: []byte("room")})
	if err != nil {
		t.Fatal(err)
	}
	wire[2] ^= 1
	if _, err = Decode(wire); !errors.Is(err, ErrCRC) {
		t.Fatalf("Decode() error = %v, want CRC error", err)
	}
}

func TestDecoderHandlesFragmentation(t *testing.T) {
	wire, err := Encode(Frame{Opcode: 6, Seq: 7, Payload: []byte("chat")})
	if err != nil {
		t.Fatal(err)
	}
	var decoder Decoder
	frames, errs := decoder.Push(wire[:3])
	if len(frames) != 0 || len(errs) != 0 {
		t.Fatal("decoded partial frame")
	}
	frames, errs = decoder.Push(wire[3:])
	if len(errs) != 0 || len(frames) != 1 || string(frames[0].Payload) != "chat" {
		t.Fatalf("frames=%#v errors=%v", frames, errs)
	}
}

func FuzzDecode(f *testing.F) {
	seed, _ := Encode(Frame{Opcode: 4, Seq: 1, Payload: []byte("seed")})
	f.Add(seed)
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, input []byte) { _, _ = Decode(input) })
}
