package game

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
	"github.com/mozzwald/slicks-server/internal/transport"
)

// Server owns every room and drives the tick loop. It implements
// transport.Handler.
type Server struct {
	rooms []*Room
	tick  time.Duration

	mu    sync.Mutex
	where map[*transport.Session]*Room

	done chan struct{}
}

// New builds a server with the given number of rooms and tick rate in Hz.
func New(rooms int, hz int) *Server {
	server := &Server{
		tick:  time.Second / time.Duration(hz),
		where: make(map[*transport.Session]*Room),
		done:  make(chan struct{}),
	}
	for index := 0; index < rooms; index++ {
		server.rooms = append(server.rooms, newRoom(byte(index), roomName(index)))
	}
	return server
}

func roomName(index int) string {
	names := []string{"Arizona", "Rally", "Stadium", "Freeway", "River", "Island"}
	if index < len(names) {
		return names[index]
	}
	return "Room " + string(rune('A'+index-len(names)))
}

// Run drives the fixed-rate tick until Stop is called.
func (server *Server) Run() {
	ticker := time.NewTicker(server.tick)
	defer ticker.Stop()
	for {
		select {
		case <-server.done:
			return
		case <-ticker.C:
			for _, room := range server.rooms {
				server.tickRoom(room)
			}
		}
	}
}

// Stop ends the tick loop.
func (server *Server) Stop() { close(server.done) }

// tickRoom advances one room's state machine and fans out SV_FRAME.
func (server *Server) tickRoom(room *Room) {
	room.mu.Lock()
	defer room.mu.Unlock()

	if room.step == proto.StepRace && !room.raceUntil.IsZero() && time.Now().After(room.raceUntil) {
		log.Printf("%s: race timed out on %s", room.Name(), room.trackName())
		room.enterResultsLocked(time.Now())
	}

	if room.step == stepResults && time.Now().After(room.stateUntil) {
		room.step = proto.StepWarmup
		room.resetCarsLocked()
		room.broadcast(proto.SVEvent, proto.Event{
			Event: proto.EventMap, Data1: room.mapID, Data2: room.step,
		}.Marshal())
	}

	now := time.Now()
	if now.Sub(room.lastReport) > RoomReport {
		room.lastReport = now
		occupied := false
		line := ""
		for index, slot := range room.slots {
			if !slot.occupied() {
				continue
			}
			occupied = true
			line += fmt.Sprintf(" [%d %s %q x=%d y=%d heard=%dms rejects=%d]",
				index, proto.PlatformName(slot.platform), proto.Trim(slot.name),
				slot.car.X, slot.car.Y, now.Sub(slot.lastHeard).Milliseconds(), slot.navRejects)
		}
		if occupied {
			log.Printf("%s state:%s", room.Name(), line)
		}
	}

	for index, slot := range room.slots {
		if !slot.occupied() {
			continue
		}
		// A slot that has joined but never spoken is loading its map with the
		// stream suspended, and cannot read a byte until it comes back.
		//
		// QuietGrace was meant to cover this, but it is measured from the last
		// thing we heard -- and handleJoin stamps lastHeard, so the gate is
		// open for the first three seconds of a load that takes about six. That
		// is ~60 frames posted at a client that is not listening. FujiNet
		// buffers the first couple of dozen, drops the rest, and delivers the
		// survivors in one burst on MOTOR re-assert; after that it stops
		// reading its UDP socket altogether and the link is deaf for good. The
		// client then sends CL_READY, never sees SV_OK, and prints
		// CONNECTION TIMED-OUT on the warmup track.
		//
		// So say nothing until the client speaks first. CL_READY sets ready and
		// CL_FRAME sets spoke; either one means somebody is on the far end with
		// the stream up. Costs a client nothing: ClientReady() transmits before
		// it listens (network.c).
		if !slot.ready && !slot.spoke {
			continue
		}
		if slot.platform == proto.PlatformLynx {
			// Answered when it speaks, not on the tick -- but never left
			// completely unanswered, or a Lynx we cannot hear becomes a Lynx
			// that cannot see.
			if now.Sub(slot.lastServed) < LynxProbe {
				continue
			}
		} else if !slot.lastHeard.IsZero() && now.Sub(slot.lastHeard) > QuietGrace {
			// Silent for longer than QuietGrace means suspended, or gone.
			// Either way there is nobody reading, and queued state is worse
			// than no state: SV_FRAME carries the latest positions, so
			// anything held back would be stale by the time it landed.
			continue
		}
		slot.lastServed = now
		// The probe is also the recovery path for a Lynx whose held-back frames
		// would otherwise never be collected, because it has stopped prompting.
		if next, ok := slot.takePending(); ok {
			slot.session.Send(next.opcode, next.payload)
			continue
		}
		// Sent even when the mask is empty. A player alone in a room excludes
		// the only occupied slot -- their own -- so skipping empty frames meant
		// the server said nothing at all to them, and NetworkUpdate() reported
		// ERR_TIMEOUT ten seconds into warmup (network.c, NET_TIMEOUT_TICKS).
		// A one-byte frame is also the keepalive the protocol otherwise lacks.
		slot.session.Send(proto.SVFrame, room.frameFor(byte(index)).Marshal())
	}
}

// stepResults extends the client's STEP_ vocabulary (definitions.h:50-51) with a
// server-only phase. Clients never see it: they are told STEP_WARMUP when the
// room returns to the lobby.
const stepResults = 3

func (room *Room) resetCarsLocked() {
	for _, slot := range room.slots {
		if !slot.occupied() {
			continue
		}
		slot.car = proto.Car{Lap: -1}
		slot.ready = false
		// A map change sends every client back through the same suspend-and-load
		// the join path does, so the fan-out gate has to shut again -- ready
		// alone would not do it, because spoke stays set from the last race and
		// either one opens the gate. Without this the server fires at a client
		// that has dropped MOTOR to read the new map off disk, which is what
		// wedged FujiNet's socket on join. carSeq/sentSeq are deliberately left
		// alone: the reset car is the (0,0) placeholder, and bumping carSeq
		// would push it out to everyone.
		slot.spoke = false
		slot.finished = false
		slot.position = 0
		slot.lapBest = 0
		slot.lapStart = time.Time{}
		slot.navRejects = 0
	}
}

// OnFrame dispatches one decoded client frame.
func (server *Server) OnFrame(session *transport.Session, frame proto.Frame) {
	switch frame.Opcode {
	case proto.CLList:
		server.handleList(session)
	case proto.CLJoin:
		server.handleJoin(session, frame)
	case proto.CLReady:
		server.handleReady(session)
	case proto.CLFrame:
		server.handleCarFrame(session, frame)
	case proto.CLEvent:
		server.handleEvent(session, frame)
	case proto.CLLeave:
		server.handleLeave(session)
	default:
		log.Printf("%s: unknown opcode %d", session.Addr(), frame.Opcode)
	}
}

// OnClose frees the sender's slot when its link drops.
func (server *Server) OnClose(session *transport.Session) {
	server.handleLeave(session)
}

func (server *Server) roomOf(session *transport.Session) *Room {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.where[session]
}

func (server *Server) handleList(session *transport.Session) {
	total := byte(len(server.rooms))
	for index, room := range server.rooms {
		entry := proto.ListEntry{Total: total, Index: byte(index), Name: room.Name()}
		if err := session.Send(proto.SVList, entry.Marshal()); err != nil {
			return
		}
	}
}

func (server *Server) handleJoin(session *transport.Session, frame proto.Frame) {
	request, err := proto.ParseJoin(frame.Payload)
	if err != nil {
		session.Send(proto.SVError, proto.MarshalError("BAD JOIN REQUEST"))
		return
	}
	if int(request.Room) >= len(server.rooms) {
		session.Send(proto.SVError, proto.MarshalError("NO SUCH ROOM"))
		return
	}
	room := server.rooms[request.Room]
	slot, ok := room.join(session, request)
	if !ok {
		session.Send(proto.SVError, proto.MarshalError("ROOM IS FULL"))
		return
	}

	server.mu.Lock()
	server.where[session] = room
	server.mu.Unlock()

	log.Printf("%s: %s joined %s as slot %d (%s)",
		session.Addr(), proto.Trim(request.Name), room.Name(), slot,
		proto.PlatformName(request.Platform))

	room.mu.Lock()
	if _, entry := room.slotOf(session); entry != nil {
		entry.lastHeard = time.Now()
	}
	room.mu.Unlock()

	session.Send(proto.SVInfo, room.info(slot).Marshal())
	server.announce(room)
}

// announce re-sends SV_INFO to everyone so name lists stay in sync.
func (server *Server) announce(room *Room) {
	room.mu.Lock()
	defer room.mu.Unlock()
	for index, slot := range room.slots {
		if !slot.occupied() {
			continue
		}
		roster := room.infoLocked(byte(index)).Marshal()
		if deferForLynx(slot, proto.SVInfo, roster) {
			continue
		}
		slot.session.Send(proto.SVInfo, roster)
	}
}

func (server *Server) handleReady(session *transport.Session) {
	room := server.roomOf(session)
	if room == nil {
		return
	}
	room.mu.Lock()
	index, slot := room.slotOf(session)
	if slot == nil {
		room.mu.Unlock()
		return
	}
	slot.ready = true
	slot.lastHeard = time.Now()
	step, mapID := room.step, room.mapID
	// Restate the roster after answering. The announce that carried a join is
	// lost on a client that was loading its map at the time -- FujiNet drops
	// most of what is sent into a suspend, and ClientReady()'s wait loop
	// discards anything that is not the answer it wants -- and SV_INFO is
	// otherwise never repeated, so that client would race seeing only its own
	// car. Sent after SV_OK, it survives the wait loop and is applied by
	// NetworkUpdate()'s SV_INFO case on the first frame of the race.
	roster := room.infoLocked(index).Marshal()
	deferred := deferForLynx(slot, proto.SVInfo, roster)
	room.mu.Unlock()

	if step == proto.StepRace {
		session.Send(proto.SVEvent, proto.Event{Event: proto.EventMap, Data1: mapID, Data2: step}.Marshal())
	} else {
		session.Send(proto.SVOK, nil)
	}

	if !deferred {
		session.Send(proto.SVInfo, roster)
	}
}

func (server *Server) handleCarFrame(session *transport.Session, frame proto.Frame) {
	room := server.roomOf(session)
	if room == nil {
		return
	}
	update, err := proto.ParseCarFrame(frame.Payload)
	if err != nil {
		return
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	index, slot := room.slotOf(session)
	if slot == nil {
		return
	}

	if !slot.spoke {
		slot.spoke = true
		log.Printf("%s: slot %d (%s) first accepted CL_FRAME: x=%d y=%d way=%d lap=%d",
			room.Name(), index, proto.PlatformName(slot.platform),
			update.Car.X, update.Car.Y, update.Car.Way, update.Car.Lap)
	}
	slot.lastHeard = time.Now()
	previous := slot.car
	slot.car = update.Car
	slot.joy = update.Joy
	slot.carSeq++

	// ComLynx is one wire shared by both directions. FujiNet writes the
	// server's bytes onto it and then reads back exactly as many to discard its
	// own echo (netstream.cpp, process_net_packet). If the Lynx happens to be
	// transmitting during that window the read swallows the Lynx's bytes
	// instead, and the frame the server finally sees is corrupt -- which is what
	// "invalid COBS frame" on a tcp:// session is. Free-running at 20 Hz in both
	// directions makes that collision routine, so the Lynx is served strictly on
	// demand: it talks, then we answer, and the bus only ever has one owner.
	if slot.platform == proto.PlatformLynx {
		slot.lastServed = time.Now()
		// One write per prompt. Sending a held-back frame and a state frame
		// back to back would hand the wire back mid-burst: the Lynx transmits
		// as soon as it hears anything (NetworkUpdate, network.c), so it would
		// answer the first write while the second was still going out. The
		// state frame it misses this round is superseded 50ms later anyway.
		if next, ok := slot.takePending(); ok {
			slot.session.Send(next.opcode, next.payload)
		} else {
			slot.session.Send(proto.SVFrame, room.frameFor(index).Marshal())
		}
	}

	// Navigation is only meaningful once the lights are out; the client does not
	// advance Vehicle.way during warmup either (game.c:824).
	if room.step != proto.StepRace {
		return
	}

	now := time.Now()
	progress := room.trackLapLocked(slot, previous, update.Car, now)
	if progress.completed {
		room.broadcast(proto.SVEvent, proto.Event{
			Event: proto.EventLap,
			Slot:  index,
			Data1: byte(progress.lapTicks),
			Data2: byte(progress.lapTicks >> 8),
		}.Marshal())
	}
	if progress.finished {
		room.finishLocked(slot)
		log.Printf("%s: slot %d finished %s in position %d (best lap %d ticks)",
			room.Name(), index, room.trackName(), slot.position, slot.lapBest)
		if room.raceOverLocked() {
			room.enterResultsLocked(now)
		}
	}
}

// trackName is the current map's name, for logging.
func (room *Room) trackName() string {
	if int(room.mapID) >= len(Tracks) {
		return "?"
	}
	return Tracks[room.mapID].Name
}

func (server *Server) handleEvent(session *transport.Session, frame proto.Frame) {
	room := server.roomOf(session)
	if room == nil {
		return
	}
	event, err := proto.ParseEvent(frame.Payload)
	if err != nil {
		return
	}
	room.mu.Lock()
	index, slot := room.slotOf(session)
	if slot == nil {
		room.mu.Unlock()
		return
	}
	event.Slot = index
	slot.lastHeard = time.Now()

	switch event.Event {
	case proto.EventRace:
		room.step = proto.StepRace
		room.raceUntil = time.Now().Add(RaceTimeout)
		for _, entry := range room.slots {
			if entry.occupied() {
				entry.finished = false
				entry.position = 0
				entry.lapBest = 0
				entry.lapStart = time.Time{}
			}
		}
		event.Data1, event.Data2 = room.mapID, room.step
	case proto.EventMap:
		room.mapID = (room.mapID + 1) % MapCount
		room.step = proto.StepWarmup
		room.resetCarsLocked()
		event.Data1, event.Data2 = room.mapID, room.step
	}
	room.broadcast(proto.SVEvent, event.Marshal())
	room.mu.Unlock()
}

func (server *Server) handleLeave(session *transport.Session) {
	room := server.roomOf(session)
	if room == nil {
		return
	}
	server.mu.Lock()
	delete(server.where, session)
	server.mu.Unlock()

	if room.leave(session) {
		log.Printf("%s: left %s", session.Addr(), room.Name())
		server.announce(room)
	}
}
