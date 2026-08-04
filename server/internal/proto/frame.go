// Package proto implements the slicks-net v1 byte-stream framing.
package proto

import "errors"

const (
	MaxPayload = 48
	MaxRaw     = 3 + MaxPayload + 2
)

var (
	ErrTooLarge = errors.New("slicks-net frame exceeds payload limit")
	ErrCOBS     = errors.New("invalid COBS frame")
	ErrLength   = errors.New("invalid slicks-net frame length")
	ErrCRC      = errors.New("invalid slicks-net frame CRC")
)

// Frame is the decoded protocol message preceding COBS framing.
type Frame struct {
	Opcode  byte
	Seq     byte
	Payload []byte
}

// CRC16CCITTFalse returns the CRC-16/CCITT-FALSE checksum for data.
func CRC16CCITTFalse(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, value := range data {
		crc ^= uint16(value) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Encode serializes a frame as COBS bytes followed by the required zero delimiter.
func Encode(frame Frame) ([]byte, error) {
	if len(frame.Payload) > MaxPayload {
		return nil, ErrTooLarge
	}
	raw := make([]byte, 0, 5+len(frame.Payload))
	raw = append(raw, frame.Opcode, frame.Seq, byte(len(frame.Payload)))
	raw = append(raw, frame.Payload...)
	checksum := CRC16CCITTFalse(raw)
	raw = append(raw, byte(checksum), byte(checksum>>8))
	encoded := cobsEncode(raw)
	return append(encoded, 0), nil
}

// Decode validates one COBS frame. Input may include its trailing delimiter.
func Decode(wire []byte) (Frame, error) {
	if len(wire) != 0 && wire[len(wire)-1] == 0 {
		wire = wire[:len(wire)-1]
	}
	raw, err := cobsDecode(wire)
	if err != nil {
		return Frame{}, err
	}
	if len(raw) < 5 || int(raw[2])+5 != len(raw) || raw[2] > MaxPayload {
		return Frame{}, ErrLength
	}
	want := uint16(raw[len(raw)-2]) | uint16(raw[len(raw)-1])<<8
	if CRC16CCITTFalse(raw[:len(raw)-2]) != want {
		return Frame{}, ErrCRC
	}
	payload := append([]byte(nil), raw[3:len(raw)-2]...)
	return Frame{Opcode: raw[0], Seq: raw[1], Payload: payload}, nil
}

// Decoder collects a raw NetStream byte stream into COBS-delimited frames.
type Decoder struct {
	buffer []byte
}

// Push returns all complete frames from input and drops malformed frames.
// Errors are reported alongside successfully decoded frames from the same input.
func (d *Decoder) Push(input []byte) ([]Frame, []error) {
	frames := make([]Frame, 0)
	errors := make([]error, 0)
	for _, value := range input {
		if value != 0 {
			if len(d.buffer) >= MaxRaw+1 {
				d.buffer = d.buffer[:0]
				errors = append(errors, ErrTooLarge)
				continue
			}
			d.buffer = append(d.buffer, value)
			continue
		}
		if len(d.buffer) == 0 {
			continue
		}
		frame, err := Decode(d.buffer)
		d.buffer = d.buffer[:0]
		if err != nil {
			errors = append(errors, err)
			continue
		}
		frames = append(frames, frame)
	}
	return frames, errors
}

func cobsEncode(input []byte) []byte {
	output := make([]byte, 1, len(input)+2)
	codeIndex := 0
	code := byte(1)
	for _, value := range input {
		if value == 0 {
			output[codeIndex] = code
			codeIndex = len(output)
			output = append(output, 0)
			code = 1
			continue
		}
		output = append(output, value)
		code++
		if code == 0xff {
			output[codeIndex] = code
			codeIndex = len(output)
			output = append(output, 0)
			code = 1
		}
	}
	output[codeIndex] = code
	return output
}

func cobsDecode(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, ErrCOBS
	}
	output := make([]byte, 0, len(input))
	for index := 0; index < len(input); {
		code := int(input[index])
		if code == 0 || index+code > len(input)+1 {
			return nil, ErrCOBS
		}
		index++
		end := index + code - 1
		if end > len(input) {
			return nil, ErrCOBS
		}
		output = append(output, input[index:end]...)
		index = end
		if code != 0xff && index < len(input) {
			output = append(output, 0)
		}
	}
	return output, nil
}
