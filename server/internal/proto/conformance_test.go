package proto

import (
	"encoding/hex"
	"testing"
)

// The client codec in projects/slicks/src/net_proto.c has to agree with this
// package byte for byte. These vectors are the output of
// projects/slicks/test/host_proto_test.c, which compiles that exact file
// natively. Regenerate with:
//
//	./projects/slicks/build_netstream.sh test
//
// A diff here means the two implementations have drifted.
func TestClientGoldenVectors(t *testing.T) {
	payload := func(build func(i int) byte, n int) []byte {
		out := make([]byte, n)
		for i := range out {
			out[i] = build(i)
		}
		return out
	}

	// NetSend increments a rolling sequence starting from zero, so the vectors
	// are ordered and each carries the sequence the client would have sent.
	cases := []struct {
		name    string
		frame   Frame
		encoded string
	}{
		{
			name:    "empty",
			frame:   Frame{Opcode: 4, Seq: 1},
			encoded: "030401036d2300",
		},
		{
			name:    "clframe",
			frame:   Frame{Opcode: 5, Seq: 2, Payload: payload(func(i int) byte { return byte(i * 17) }, 11)},
			encoded: "0405020b0d112233445566778899aa286300",
		},
		{
			// An all-zero payload is the worst case for COBS.
			name:    "zeros",
			frame:   Frame{Opcode: 3, Seq: 3, Payload: make([]byte, MaxPayload)},
			encoded: "04030330010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010103607400",
		},
		{
			name:    "maxpayload",
			frame:   Frame{Opcode: 1, Seq: 4, Payload: payload(func(i int) byte { return byte(255 - i) }, MaxPayload)},
			encoded: "36010430fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0efeeedecebeae9e8e7e6e5e4e3e2e1e0dfdedddcdbdad9d8d7d6d5d4d3d2d1d0ead900",
		},
	}

	for _, test := range cases {
		want, err := hex.DecodeString(test.encoded)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Encode(test.frame)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if hex.EncodeToString(got) != test.encoded {
			t.Errorf("%s: Encode() = %s, client emits %s",
				test.name, hex.EncodeToString(got), test.encoded)
		}
		back, err := Decode(want)
		if err != nil {
			t.Fatalf("%s: decoding the client's bytes: %v", test.name, err)
		}
		if back.Opcode != test.frame.Opcode || back.Seq != test.frame.Seq {
			t.Errorf("%s: decoded header = %d/%d, want %d/%d",
				test.name, back.Opcode, back.Seq, test.frame.Opcode, test.frame.Seq)
		}
		if len(back.Payload) != len(test.frame.Payload) {
			t.Errorf("%s: decoded payload is %d bytes, want %d",
				test.name, len(back.Payload), len(test.frame.Payload))
		}
	}
}

// The CCITT-FALSE check value is the standard's own conformance vector.
func TestCRCCheckValue(t *testing.T) {
	if got := CRC16CCITTFalse([]byte("123456789")); got != 0x29b1 {
		t.Fatalf("CRC16CCITTFalse(\"123456789\") = %04x, want 29b1", got)
	}
	if got := CRC16CCITTFalse(nil); got != 0xffff {
		t.Fatalf("CRC16CCITTFalse(nil) = %04x, want ffff", got)
	}
}
