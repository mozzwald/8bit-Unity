package proto

import "errors"

const (
	CLError  = 0
	CLList   = 1
	CLJoin   = 2
	CLLeave  = 3
	CLReady  = 4
	CLFrame  = 5
	CLEvent  = 6
	CLTicket = 7

	SVError = 0
	SVList  = 1
	SVAuth  = 2
	SVInfo  = 3
	SVFrame = 4
	SVEvent = 5
	SVOK    = 6

	EventRace = 1
	EventMap  = 2
	EventLap  = 3
	EventChat = 4

	ErrTimeout = 127
	ErrCorrupt = 128
	ErrMessage = 129

	StepWarmup = 1
	StepRace   = 2

	ProtocolVersion = 1
	NameBytes       = 9
	MaxSlots        = 4

	// SlotEmpty and SlotTaken are the SV_INFO occupancy values. The client maps
	// SlotTaken to its own platform-specific NET_CONTROL, which varies with
	// LEN_CONTROL (definitions.h:160-189), so the server never names it.
	SlotEmpty = 0
	SlotTaken = 1

	// PlatformAtari and PlatformLynx identify the client in CL_JOIN.
	PlatformAtari = 1
	PlatformLynx  = 2
)

var ErrPayload = errors.New("invalid slicks-net payload")

// Join is the CL_JOIN payload. Ticket zero represents the anonymous Phase 1 mode.
type Join struct {
	Ticket   uint32
	Platform byte
	Version  byte
	Room     byte
	Name     [NameBytes]byte
}

func ParseJoin(payload []byte) (Join, error) {
	if len(payload) != 4+1+1+1+NameBytes {
		return Join{}, ErrPayload
	}
	join := Join{
		Ticket:   uint32(payload[0]) | uint32(payload[1])<<8 | uint32(payload[2])<<16 | uint32(payload[3])<<24,
		Platform: payload[4], Version: payload[5], Room: payload[6],
	}
	copy(join.Name[:], payload[7:])
	if join.Version != ProtocolVersion {
		return Join{}, ErrPayload
	}
	return join, nil
}

func (join Join) Marshal() []byte {
	payload := make([]byte, 4+1+1+1+NameBytes)
	payload[0], payload[1], payload[2], payload[3] = byte(join.Ticket), byte(join.Ticket>>8), byte(join.Ticket>>16), byte(join.Ticket>>24)
	payload[4], payload[5], payload[6] = join.Platform, join.Version, join.Room
	copy(payload[7:], join.Name[:])
	return payload
}

// PlatformName renders a CL_JOIN platform byte for logging.
func PlatformName(platform byte) string {
	switch platform {
	case PlatformAtari:
		return "atari"
	case PlatformLynx:
		return "lynx"
	default:
		return "unknown"
	}
}

// Trim returns the printable prefix of a fixed-width NUL-padded name field.
func Trim(name [NameBytes]byte) string {
	for index, value := range name {
		if value == 0 {
			return string(name[:index])
		}
	}
	return string(name[:])
}

// SlotInfo is one player portion of SV_INFO.
type SlotInfo struct {
	Control byte
	Name    [NameBytes]byte
}

// Info is the authoritative room snapshot sent after a successful join.
type Info struct {
	Slot, Map, Step, LapGoal byte
	Slots                    [MaxSlots]SlotInfo
}

func ParseInfo(payload []byte) (Info, error) {
	if len(payload) != 4+MaxSlots*(1+NameBytes) {
		return Info{}, ErrPayload
	}
	info := Info{Slot: payload[0], Map: payload[1], Step: payload[2], LapGoal: payload[3]}
	offset := 4
	for index := range info.Slots {
		info.Slots[index].Control = payload[offset]
		copy(info.Slots[index].Name[:], payload[offset+1:offset+1+NameBytes])
		offset += 1 + NameBytes
	}
	return info, nil
}

func (info Info) Marshal() []byte {
	payload := make([]byte, 4+len(info.Slots)*(1+NameBytes))
	payload[0], payload[1], payload[2], payload[3] = info.Slot, info.Map, info.Step, info.LapGoal
	offset := 4
	for _, slot := range info.Slots {
		payload[offset] = slot.Control
		copy(payload[offset+1:], slot.Name[:])
		offset += 1 + NameBytes
	}
	return payload
}

// CarBytes is the wire width of one car's state inside SV_FRAME.
const CarBytes = 10

// Car is a single vehicle's replicated state. Fields mirror the Vehicle struct
// at projects/slicks/src/definitions.h:218-232.
//
// Ang is the client's ang2, the trajectory angle it draws its own sprite from --
// not ang1. A receiver derives ang1 from ang2 by lerping (game.c), so ang1 is
// reconstructable and ang2 is not: nothing else on the wire moves it. The server
// only relays the value, so the choice is the clients' to agree on.
type Car struct {
	X, Y, Ang, Vel int16
	Way            byte
	Lap            int8
}

func putInt16(target []byte, value int16) {
	target[0], target[1] = byte(value), byte(value>>8)
}

func getInt16(source []byte) int16 {
	return int16(uint16(source[0]) | uint16(source[1])<<8)
}

// CarFrame is the CL_FRAME payload: the sender's own car plus its joystick state.
type CarFrame struct {
	Joy byte
	Car Car
}

func (frame CarFrame) Marshal() []byte {
	payload := make([]byte, 1+CarBytes)
	payload[0] = frame.Joy
	frame.Car.marshalInto(payload[1:])
	return payload
}

func ParseCarFrame(payload []byte) (CarFrame, error) {
	if len(payload) != 1+CarBytes {
		return CarFrame{}, ErrPayload
	}
	frame := CarFrame{Joy: payload[0]}
	frame.Car.parseFrom(payload[1:])
	return frame, nil
}

func (car Car) marshalInto(target []byte) {
	putInt16(target[0:], car.X)
	putInt16(target[2:], car.Y)
	putInt16(target[4:], car.Ang)
	putInt16(target[6:], car.Vel)
	target[8], target[9] = car.Way, byte(car.Lap)
}

func (car *Car) parseFrom(source []byte) {
	car.X = getInt16(source[0:])
	car.Y = getInt16(source[2:])
	car.Ang = getInt16(source[4:])
	car.Vel = getInt16(source[6:])
	car.Way, car.Lap = source[8], int8(source[9])
}

// StateFrame is the SV_FRAME payload: a slot mask followed by one Car per set bit.
// At four players this is 41 bytes, inside the 48-byte payload cap.
type StateFrame struct {
	Mask byte
	Cars [MaxSlots]Car
}

func (frame StateFrame) Marshal() []byte {
	payload := make([]byte, 1, 1+MaxSlots*CarBytes)
	payload[0] = frame.Mask
	for slot := 0; slot < MaxSlots; slot++ {
		if frame.Mask&(1<<uint(slot)) == 0 {
			continue
		}
		offset := len(payload)
		payload = append(payload, make([]byte, CarBytes)...)
		frame.Cars[slot].marshalInto(payload[offset:])
	}
	return payload
}

func ParseStateFrame(payload []byte) (StateFrame, error) {
	if len(payload) == 0 {
		return StateFrame{}, ErrPayload
	}
	frame := StateFrame{Mask: payload[0]}
	if frame.Mask>>MaxSlots != 0 {
		return StateFrame{}, ErrPayload
	}
	offset := 1
	for slot := 0; slot < MaxSlots; slot++ {
		if frame.Mask&(1<<uint(slot)) == 0 {
			continue
		}
		if offset+CarBytes > len(payload) {
			return StateFrame{}, ErrPayload
		}
		frame.Cars[slot].parseFrom(payload[offset:])
		offset += CarBytes
	}
	if offset != len(payload) {
		return StateFrame{}, ErrPayload
	}
	return frame, nil
}

// Event carries CL_EVENT and SV_EVENT. Text is used only by EVENT_CHAT and is
// NUL-terminated on the wire so the client can print it in place.
type Event struct {
	Event, Slot, Data1, Data2 byte
	Text                      string
}

func (event Event) Marshal() []byte {
	payload := []byte{event.Event, event.Slot, event.Data1, event.Data2}
	if event.Text == "" {
		return payload
	}
	limit := MaxPayload - len(payload) - 1
	text := event.Text
	if len(text) > limit {
		text = text[:limit]
	}
	return append(append(payload, text...), 0)
}

func ParseEvent(payload []byte) (Event, error) {
	if len(payload) < 4 {
		return Event{}, ErrPayload
	}
	event := Event{Event: payload[0], Slot: payload[1], Data1: payload[2], Data2: payload[3]}
	text := payload[4:]
	if index := indexZero(text); index >= 0 {
		text = text[:index]
	}
	event.Text = sanitize(text)
	return event, nil
}

func indexZero(data []byte) int {
	for index, value := range data {
		if value == 0 {
			return index
		}
	}
	return -1
}

// sanitize drops control bytes so a hostile client cannot inject the newline
// that terminates names in the legacy SV_LIST layout, nor a NUL mid-string.
func sanitize(data []byte) string {
	out := make([]byte, 0, len(data))
	for _, value := range data {
		if value >= 0x20 && value < 0x7f {
			out = append(out, value)
		}
	}
	return string(out)
}

// ListEntry is one SV_LIST frame. The full room list cannot fit the 48-byte
// payload cap (12 rooms x 16 bytes), so the server sends one frame per room and
// the client accumulates them into the legacy buffer that interface.c:719-751
// parses: byte0 = SV_LIST, byte1 = count, then newline-terminated names.
type ListEntry struct {
	Total, Index byte
	Name         string
}

// ListNameMax matches the 15-char truncation the legacy menu buffer applies.
const ListNameMax = 15

func (entry ListEntry) Marshal() []byte {
	name := entry.Name
	if len(name) > ListNameMax {
		name = name[:ListNameMax]
	}
	payload := []byte{entry.Total, entry.Index}
	return append(append(payload, sanitize([]byte(name))...), 0)
}

func ParseListEntry(payload []byte) (ListEntry, error) {
	if len(payload) < 3 || payload[len(payload)-1] != 0 {
		return ListEntry{}, ErrPayload
	}
	entry := ListEntry{Total: payload[0], Index: payload[1]}
	if entry.Index >= entry.Total {
		return ListEntry{}, ErrPayload
	}
	entry.Name = sanitize(payload[2 : len(payload)-1])
	return entry, nil
}

// MarshalError builds the SV_ERROR payload. interface.c:808 prints
// (char*)(packet+1), so the text must be NUL-terminated at offset 1 of the
// buffer the client points `packet` at.
func MarshalError(message string) []byte {
	limit := MaxPayload - 1
	if len(message) > limit {
		message = message[:limit]
	}
	return append(append([]byte(nil), sanitize([]byte(message))...), 0)
}
