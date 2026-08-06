package game

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
	"github.com/mozzwald/slicks-server/internal/transport"
)

// testEndpoint finds a port in the project's reserved 8300-8399 range where both
// the UDP and TCP listeners bind, since transport.Listen needs the same number
// for both and port 0 would hand out two different ones.
func testEndpoint(t *testing.T, server *Server) (*transport.Listener, string) {
	t.Helper()
	for port := 8380; port < 8400; port++ {
		endpoint := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		listener, err := transport.Listen(endpoint, server)
		if err == nil {
			return listener, endpoint
		}
	}
	t.Fatal("no free port in 8380-8399")
	return nil, ""
}

// harness is a minimal in-process client for driving the server.
type harness struct {
	t        *testing.T
	conn     net.Conn
	decoder  proto.Decoder
	sequence byte
	pending  []proto.Frame
}

func dial(t *testing.T, network, endpoint string) *harness {
	t.Helper()
	conn, err := net.Dial(network, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &harness{t: t, conn: conn}
}

func (h *harness) send(opcode byte, payload []byte) {
	h.t.Helper()
	h.sequence++
	wire, err := proto.Encode(proto.Frame{Opcode: opcode, Seq: h.sequence, Payload: payload})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.conn.Write(wire); err != nil {
		h.t.Fatal(err)
	}
}

// await reads until a frame with the wanted opcode arrives or the deadline passes.
func (h *harness) await(opcode byte) proto.Frame {
	h.t.Helper()
	for index, frame := range h.pending {
		if frame.Opcode == opcode {
			h.pending = append(h.pending[:index], h.pending[index+1:]...)
			return frame
		}
	}
	buf := make([]byte, 2048)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.conn.SetReadDeadline(deadline)
		n, err := h.conn.Read(buf)
		if err != nil {
			h.t.Fatalf("waiting for opcode %d: %v", opcode, err)
		}
		frames, errs := h.decoder.Push(buf[:n])
		for _, err := range errs {
			h.t.Fatalf("decode: %v", err)
		}
		for _, frame := range frames {
			if frame.Opcode == opcode {
				return frame
			}
			h.pending = append(h.pending, frame)
		}
	}
	h.t.Fatalf("timed out waiting for opcode %d", opcode)
	return proto.Frame{}
}

func (h *harness) join(name string, room byte, ticket uint32, platform byte) proto.Info {
	h.t.Helper()
	request := proto.Join{Ticket: ticket, Platform: platform, Version: proto.ProtocolVersion, Room: room}
	copy(request.Name[:], name)
	h.send(proto.CLJoin, request.Marshal())
	info, err := proto.ParseInfo(h.await(proto.SVInfo).Payload)
	if err != nil {
		h.t.Fatal(err)
	}
	return info
}

func start(t *testing.T) (*Server, string) {
	t.Helper()
	server := New(4, 20)
	listener, endpoint := testEndpoint(t, server)
	go server.Run()
	t.Cleanup(func() {
		server.Stop()
		listener.Close()
	})
	return server, endpoint
}

func TestFourPlayersShareOneRoom(t *testing.T) {
	_, endpoint := start(t)

	names := []string{"MOZZWALD", "PLAYER2", "P3", "FOURTH"}
	clients := make([]*harness, len(names))
	for index, name := range names {
		clients[index] = dial(t, "udp", endpoint)
		info := clients[index].join(name, 0, 0, proto.PlatformAtari)
		if int(info.Slot) != index {
			t.Fatalf("%s got slot %d, want %d", name, info.Slot, index)
		}
		if info.Step != proto.StepWarmup {
			t.Fatalf("joined into step %d, want STEP_WARMUP", info.Step)
		}
	}

	// Every join re-broadcasts SV_INFO, so the first player sees a series of
	// snapshots. Read forward until the room is full, then check the names.
	var parsed proto.Info
	for attempt := 0; attempt < len(names); attempt++ {
		snapshot, err := proto.ParseInfo(clients[0].await(proto.SVInfo).Payload)
		if err != nil {
			t.Fatal(err)
		}
		parsed = snapshot
		if snapshot.Slots[len(names)-1].Control == proto.SlotTaken {
			break
		}
	}
	for index, name := range names {
		if parsed.Slots[index].Control != proto.SlotTaken {
			t.Fatalf("slot %d reads empty", index)
		}
		if got := proto.Trim(parsed.Slots[index].Name); got != name {
			t.Fatalf("slot %d name = %q, want %q", index, got, name)
		}
	}
}

func TestRoomRejectsFifthPlayer(t *testing.T) {
	_, endpoint := start(t)
	for index := 0; index < proto.MaxSlots; index++ {
		dial(t, "udp", endpoint).join("P"+strconv.Itoa(index), 0, 0, proto.PlatformAtari)
	}
	extra := dial(t, "udp", endpoint)
	request := proto.Join{Version: proto.ProtocolVersion}
	extra.send(proto.CLJoin, request.Marshal())
	payload := extra.await(proto.SVError).Payload
	if len(payload) == 0 || payload[len(payload)-1] != 0 {
		t.Fatal("SV_ERROR must be NUL terminated")
	}
}

func TestCarStateReachesTheOtherPlayer(t *testing.T) {
	_, endpoint := start(t)
	first := dial(t, "udp", endpoint)
	first.join("FIRST", 0, 0, proto.PlatformAtari)
	second := dial(t, "udp", endpoint)
	second.join("SECOND", 0, 0, proto.PlatformAtari)

	car := proto.Car{X: 4321, Y: -1234, Ang1: 180, Vel: 96, Way: 3, Lap: 1}
	second.send(proto.CLFrame, proto.CarFrame{Joy: 5, Car: car}.Marshal())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, err := proto.ParseStateFrame(first.await(proto.SVFrame).Payload)
		if err != nil {
			t.Fatal(err)
		}
		if state.Mask&0x01 != 0 {
			t.Fatal("SV_FRAME must omit the recipient's own slot")
		}
		if state.Mask&0x02 != 0 && state.Cars[1] == car {
			return
		}
	}
	t.Fatal("the second player's car never arrived in an SV_FRAME")
}

func TestRejoinWithTokenKeepsTheSameSlot(t *testing.T) {
	_, endpoint := start(t)
	dial(t, "udp", endpoint).join("FIRST", 0, 0, proto.PlatformAtari)

	const token = 0xdeadbeef
	original := dial(t, "udp", endpoint)
	if slot := original.join("RESUMER", 0, token, proto.PlatformAtari).Slot; slot != 1 {
		t.Fatalf("first join took slot %d, want 1", slot)
	}

	// A NetStream suspend/resume reappears as a new UDP source port. The token
	// must re-bind the existing slot rather than consuming a fresh one.
	resumed := dial(t, "udp", endpoint)
	if slot := resumed.join("RESUMER", 0, token, proto.PlatformAtari).Slot; slot != 1 {
		t.Fatalf("rejoin took slot %d, want its original slot 1", slot)
	}
}

func TestRoomListCoversEveryRoom(t *testing.T) {
	server, endpoint := start(t)
	client := dial(t, "udp", endpoint)
	client.send(proto.CLList, nil)

	seen := make(map[byte]string)
	for len(seen) < len(server.rooms) {
		entry, err := proto.ParseListEntry(client.await(proto.SVList).Payload)
		if err != nil {
			t.Fatal(err)
		}
		if int(entry.Total) != len(server.rooms) {
			t.Fatalf("entry reports %d rooms, want %d", entry.Total, len(server.rooms))
		}
		seen[entry.Index] = entry.Name
	}
	for index, room := range server.rooms {
		if seen[byte(index)] != room.Name() {
			t.Fatalf("room %d listed as %q, want %q", index, seen[byte(index)], room.Name())
		}
	}
}

func TestChatIsRelayedTaggedWithTheSenderSlot(t *testing.T) {
	_, endpoint := start(t)
	first := dial(t, "udp", endpoint)
	first.join("FIRST", 0, 0, proto.PlatformAtari)
	second := dial(t, "udp", endpoint)
	second.join("SECOND", 0, 0, proto.PlatformAtari)

	second.send(proto.CLEvent, proto.Event{Event: proto.EventChat, Text: "HELLO"}.Marshal())
	event, err := proto.ParseEvent(first.await(proto.SVEvent).Payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != proto.EventChat || event.Text != "HELLO" {
		t.Fatalf("relayed event = %#v, want the chat text", event)
	}
	if event.Slot != 1 {
		t.Fatalf("chat tagged with slot %d, want the sender's slot 1", event.Slot)
	}
}

func TestRaceEventAdvancesTheRoomStep(t *testing.T) {
	server, endpoint := start(t)
	client := dial(t, "udp", endpoint)
	client.join("HOST", 0, 0, proto.PlatformAtari)

	client.send(proto.CLEvent, proto.Event{Event: proto.EventRace}.Marshal())
	event, err := proto.ParseEvent(client.await(proto.SVEvent).Payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != proto.EventRace || event.Data2 != proto.StepRace {
		t.Fatalf("EVENT_RACE relayed as %#v, want step STEP_RACE", event)
	}
	server.rooms[0].mu.Lock()
	step := server.rooms[0].step
	server.rooms[0].mu.Unlock()
	if step != proto.StepRace {
		t.Fatalf("room step = %d, want STEP_RACE", step)
	}
}

func TestMapEventAdvancesTheMapAndReturnsToWarmup(t *testing.T) {
	server, endpoint := start(t)
	client := dial(t, "udp", endpoint)
	start := client.join("HOST", 0, 0, proto.PlatformAtari)

	client.send(proto.CLEvent, proto.Event{Event: proto.EventMap}.Marshal())
	event, err := proto.ParseEvent(client.await(proto.SVEvent).Payload)
	if err != nil {
		t.Fatal(err)
	}
	want := (start.Map + 1) % MapCount
	if event.Data1 != want {
		t.Fatalf("EVENT_MAP advanced to map %d, want %d", event.Data1, want)
	}
	if event.Data2 != proto.StepWarmup {
		t.Fatalf("EVENT_MAP left step %d, want STEP_WARMUP", event.Data2)
	}
	server.rooms[0].mu.Lock()
	mapID := server.rooms[0].mapID
	server.rooms[0].mu.Unlock()
	if mapID != want {
		t.Fatalf("room map = %d, want %d", mapID, want)
	}
}

func TestLynxOverTCPSharesARoomWithAtariOverUDP(t *testing.T) {
	_, endpoint := start(t)
	atari := dial(t, "udp", endpoint)
	atari.join("ATARI", 0, 0, proto.PlatformAtari)
	lynx := dial(t, "tcp", endpoint)
	info := lynx.join("LYNX", 0, 0, proto.PlatformLynx)

	if info.Slot != 1 {
		t.Fatalf("Lynx took slot %d, want 1", info.Slot)
	}
	if got := proto.Trim(info.Slots[0].Name); got != "ATARI" {
		t.Fatalf("Lynx sees slot 0 as %q, want the Atari player", got)
	}
}

func TestLeaveFreesTheSlot(t *testing.T) {
	server, endpoint := start(t)
	client := dial(t, "udp", endpoint)
	client.join("QUITTER", 0, 0, proto.PlatformAtari)
	client.send(proto.CLLeave, nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.rooms[0].mu.Lock()
		occupied := server.rooms[0].slots[0].occupied()
		server.rooms[0].mu.Unlock()
		if !occupied {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("CL_LEAVE did not free the slot")
}

// A single player in a room must still receive SV_FRAME. The state frame omits
// the recipient's own slot, so a lone racer's mask is empty -- and while empty
// frames were skipped the server sent them nothing at all. The client counts
// that as a dead link and reports ERR_TIMEOUT ten seconds into warmup.
func TestLoneRacerReceivesFrames(t *testing.T) {
	server := New(1, 20)
	listener, endpoint := testEndpoint(t, server)
	defer listener.Close()
	go server.Run()
	defer server.Stop()

	client := dial(t, "udp", endpoint)
	defer client.conn.Close()
	client.join("SOLO", 0, 0, proto.PlatformAtari)

	// Two ticks at 20 Hz is 100ms; allow plenty of slack for a loaded machine.
	client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	frame := client.await(proto.SVFrame)

	if len(frame.Payload) == 0 {
		t.Fatal("SV_FRAME arrived with no payload")
	}
	if frame.Payload[0] != 0 {
		t.Fatalf("mask = %d, want 0 with nobody else in the room", frame.Payload[0])
	}
}

// An Atari drops MOTOR and goes silent for seconds while it loads a map, and it
// cannot receive during that window. Firing state at it the whole time makes
// FujiNet queue what fits, drop the rest, and deliver the survivors as one burst
// the client's ring cannot absorb -- observed as drops=28/count=32 followed by a
// client that never spoke again.
func TestQuietSlotStopsReceivingFrames(t *testing.T) {
	server := New(1, 20)
	listener, endpoint := testEndpoint(t, server)
	defer listener.Close()
	go server.Run()
	defer server.Stop()

	client := dial(t, "udp", endpoint)
	defer client.conn.Close()
	client.join("QUIET", 0, 0, proto.PlatformAtari)

	client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	client.await(proto.SVFrame) // talking, so served

	// Go quiet the way a map load does, then drain whatever was already in
	// flight and confirm the server has stopped adding to it.
	time.Sleep(QuietGrace + 250*time.Millisecond)
	client.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		var buffer [256]byte
		if _, err := client.conn.Read(buffer[:]); err != nil {
			break
		}
	}
	client.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buffer [256]byte
	if n, err := client.conn.Read(buffer[:]); err == nil {
		t.Fatalf("server sent %d bytes to a slot that has been silent for %v", n, QuietGrace)
	}

	// Speaking up resumes the fan-out.
	client.send(proto.CLFrame, make([]byte, 11))
	client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	client.await(proto.SVFrame)
}

// ComLynx is half duplex on a single wire, so the server must not transmit to a
// Lynx unprompted: FujiNet's echo-discard read swallows whatever the Lynx was
// sending at the time, and every frame the server sees is corrupt. A Lynx is
// answered only when it speaks.
func TestLynxIsServedOnDemandNotOnTick(t *testing.T) {
	server := New(1, 20)
	listener, endpoint := testEndpoint(t, server)
	defer listener.Close()
	go server.Run()
	defer server.Stop()

	client := dial(t, "tcp", endpoint)
	defer client.conn.Close()
	client.join("LYNX", 0, 0, proto.PlatformLynx)

	// Several ticks pass with the client silent; nothing may arrive.
	client.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		var buffer [256]byte
		if _, err := client.conn.Read(buffer[:]); err != nil {
			break
		}
	}
	client.conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	var buffer [256]byte
	if n, err := client.conn.Read(buffer[:]); err == nil {
		t.Fatalf("server sent %d unprompted bytes to a Lynx", n)
	}

	// Speaking gets exactly one answer.
	client.send(proto.CLFrame, make([]byte, 11))
	client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	client.await(proto.SVFrame)
}

// The Atari is full duplex over SIO and keeps its free-running fan-out.
func TestAtariStillServedOnTheTick(t *testing.T) {
	server := New(1, 20)
	listener, endpoint := testEndpoint(t, server)
	defer listener.Close()
	go server.Run()
	defer server.Stop()

	client := dial(t, "udp", endpoint)
	defer client.conn.Close()
	client.join("ATARI", 0, 0, proto.PlatformAtari)

	client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	client.await(proto.SVFrame)
}

// The Atari never showed the Lynx's car. That could be the Lynx failing to
// transmit, or the server failing to relay -- this pins the second half down:
// a CL_FRAME from the Lynx must reach the Atari as an SV_FRAME whose mask names
// the Lynx's slot and whose bytes are the Car layout network.c decodes.
func TestLynxCarReachesTheAtari(t *testing.T) {
	server := New(1, 20)
	listener, endpoint := testEndpoint(t, server)
	defer listener.Close()
	go server.Run()
	defer server.Stop()

	atari := dial(t, "udp", endpoint)
	defer atari.conn.Close()
	atari.join("ATARI", 0, 0, proto.PlatformAtari)

	lynx := dial(t, "tcp", endpoint)
	defer lynx.conn.Close()
	lynx.join("LYNX", 0, 0, proto.PlatformLynx)

	// The Atari has to stay "heard" or the quiet-slot gate stops serving it.
	atari.send(proto.CLFrame, make([]byte, 11))

	// A recognisable position from the Lynx: x=0x1234, y=0x5678.
	car := proto.CarFrame{Joy: 0, Car: proto.Car{
		X: 0x1234, Y: 0x5678, Ang1: 90, Vel: 7, Way: 3, Lap: 1,
	}}
	lynx.send(proto.CLFrame, car.Marshal())

	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the Lynx's car never reached the Atari")
		}
		atari.send(proto.CLFrame, make([]byte, 11))
		atari.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		frame, ok := func() (proto.Frame, bool) {
			defer func() { recover() }()
			return atari.await(proto.SVFrame), true
		}()
		if !ok || len(frame.Payload) < 1 {
			continue
		}
		if frame.Payload[0]&(1<<1) == 0 {
			continue // mask does not name the Lynx yet
		}
		// Slot 0 is excluded, so the Lynx's Car starts right after the mask.
		if len(frame.Payload) < 1+proto.CarBytes {
			t.Fatalf("SV_FRAME too short for a car: %d bytes", len(frame.Payload))
		}
		got, err := proto.ParseCarFrame(append([]byte{0}, frame.Payload[1:1+proto.CarBytes]...))
		if err != nil {
			t.Fatalf("car payload: %v", err)
		}
		if got.Car.X != 0x1234 || got.Car.Y != 0x5678 {
			t.Fatalf("relayed car = (%#x,%#x), want (0x1234,0x5678)", got.Car.X, got.Car.Y)
		}
		return
	}
}
