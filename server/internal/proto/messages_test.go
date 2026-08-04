package proto

import "testing"

func TestJoinRoundTrip(t *testing.T) {
	join := Join{Ticket: 0x12345678, Platform: 1, Version: ProtocolVersion, Room: 2}
	copy(join.Name[:], "Slicks8")
	got, err := ParseJoin(join.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != join {
		t.Fatalf("ParseJoin() = %#v, want %#v", got, join)
	}
}

func TestParseJoinRejectsWrongVersion(t *testing.T) {
	join := Join{Version: 2}
	if _, err := ParseJoin(join.Marshal()); err == nil {
		t.Fatal("ParseJoin accepted wrong protocol version")
	}
}

func TestCarFrameRoundTrip(t *testing.T) {
	frame := CarFrame{Joy: 0x0f, Car: Car{X: -1234, Y: 30000, Ang1: 359, Vel: -64, Way: 7, Lap: -1}}
	got, err := ParseCarFrame(frame.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != frame {
		t.Fatalf("ParseCarFrame() = %#v, want %#v", got, frame)
	}
}

func TestCarFrameRejectsShortPayload(t *testing.T) {
	if _, err := ParseCarFrame(make([]byte, CarBytes)); err == nil {
		t.Fatal("ParseCarFrame accepted a truncated payload")
	}
}

func TestStateFrameRoundTripEverySlotCombination(t *testing.T) {
	for mask := 0; mask < 1<<MaxSlots; mask++ {
		frame := StateFrame{Mask: byte(mask)}
		for slot := 0; slot < MaxSlots; slot++ {
			if mask&(1<<uint(slot)) != 0 {
				frame.Cars[slot] = Car{X: int16(slot * 100), Y: int16(-slot), Ang1: 90, Vel: 8, Way: byte(slot), Lap: int8(slot)}
			}
		}
		wire := frame.Marshal()
		if len(wire) > MaxPayload {
			t.Fatalf("mask %04b marshals to %d bytes, over the %d cap", mask, len(wire), MaxPayload)
		}
		got, err := ParseStateFrame(wire)
		if err != nil {
			t.Fatalf("mask %04b: %v", mask, err)
		}
		if got != frame {
			t.Fatalf("mask %04b round trip = %#v, want %#v", mask, got, frame)
		}
	}
}

func TestStateFrameRejectsMaskWithoutCars(t *testing.T) {
	if _, err := ParseStateFrame([]byte{0x0f}); err == nil {
		t.Fatal("ParseStateFrame accepted a mask with no car data")
	}
}

func TestStateFrameRejectsUndefinedMaskBits(t *testing.T) {
	if _, err := ParseStateFrame([]byte{0xf0}); err == nil {
		t.Fatal("ParseStateFrame accepted mask bits above MaxSlots")
	}
}

func TestInfoRoundTrip(t *testing.T) {
	info := Info{Slot: 2, Map: 5, Step: StepRace, LapGoal: 3}
	info.Slots[0] = SlotInfo{Control: SlotTaken}
	copy(info.Slots[0].Name[:], "MOZZWALD")
	info.Slots[2] = SlotInfo{Control: SlotTaken}
	copy(info.Slots[2].Name[:], "AB")
	got, err := ParseInfo(info.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != info {
		t.Fatalf("ParseInfo() = %#v, want %#v", got, info)
	}
	if name := Trim(info.Slots[0].Name); name != "MOZZWALD" {
		t.Fatalf("Trim() = %q, want the full 8-character name", name)
	}
}

func TestInfoFitsPayloadCap(t *testing.T) {
	if size := len(Info{}.Marshal()); size > MaxPayload {
		t.Fatalf("SV_INFO is %d bytes, over the %d cap", size, MaxPayload)
	}
}

func TestEventChatRoundTrip(t *testing.T) {
	event := Event{Event: EventChat, Slot: 1, Data1: 2, Data2: 3, Text: "GG"}
	got, err := ParseEvent(event.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != event {
		t.Fatalf("ParseEvent() = %#v, want %#v", got, event)
	}
}

func TestEventStripsControlBytes(t *testing.T) {
	// A newline would corrupt the legacy SV_LIST name parser and a NUL would
	// truncate the string the client prints.
	event := Event{Event: EventChat, Text: "a\nb\x00c"}
	got, err := ParseEvent(event.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "ab" {
		t.Fatalf("ParseEvent text = %q, want control bytes dropped", got.Text)
	}
}

func TestEventTextTruncatedToPayloadCap(t *testing.T) {
	long := ""
	for len(long) < 200 {
		long += "x"
	}
	if size := len(Event{Event: EventChat, Text: long}.Marshal()); size > MaxPayload {
		t.Fatalf("chat event is %d bytes, over the %d cap", size, MaxPayload)
	}
}

func TestListEntryRoundTrip(t *testing.T) {
	entry := ListEntry{Total: 4, Index: 1, Name: "Rally"}
	got, err := ParseListEntry(entry.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != entry {
		t.Fatalf("ParseListEntry() = %#v, want %#v", got, entry)
	}
}

func TestListEntryTruncatesToLegacyWidth(t *testing.T) {
	entry := ListEntry{Total: 1, Index: 0, Name: "A room name far past the legacy limit"}
	got, err := ParseListEntry(entry.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Name) != ListNameMax {
		t.Fatalf("name length = %d, want %d", len(got.Name), ListNameMax)
	}
}

func TestListEntryRejectsIndexBeyondTotal(t *testing.T) {
	if _, err := ParseListEntry([]byte{2, 2, 0}); err == nil {
		t.Fatal("ParseListEntry accepted an index at or past total")
	}
}

func TestMarshalErrorIsNULTerminated(t *testing.T) {
	payload := MarshalError("ROOM IS FULL")
	if payload[len(payload)-1] != 0 {
		t.Fatal("SV_ERROR payload must be NUL terminated for interface.c:808")
	}
	if len(payload) > MaxPayload {
		t.Fatalf("SV_ERROR is %d bytes, over the %d cap", len(payload), MaxPayload)
	}
}
