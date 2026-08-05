package game

import (
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

	for index, slot := range room.slots {
		if !slot.occupied() {
			continue
		}
		// Sent even when the mask is empty. A player alone in a room excludes
		// the only occupied slot -- their own -- so skipping empty frames meant
		// the server said nothing at all to them, and NetworkUpdate() reported
		// ERR_TIMEOUT ten seconds into warmup (network.c, NET_TIMEOUT_TICKS).
		// A one-byte frame is also the keepalive the protocol otherwise lacks.
		slot.session.Send(proto.SVFrame, room.stateFrame(byte(index)).Marshal())
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

	session.Send(proto.SVInfo, room.info(slot).Marshal())
	server.announce(room)
}

// announce re-sends SV_INFO to everyone so name lists stay in sync.
func (server *Server) announce(room *Room) {
	room.mu.Lock()
	defer room.mu.Unlock()
	for index, slot := range room.slots {
		if slot.occupied() {
			slot.session.Send(proto.SVInfo, room.infoLocked(byte(index)).Marshal())
		}
	}
}

func (server *Server) handleReady(session *transport.Session) {
	room := server.roomOf(session)
	if room == nil {
		return
	}
	room.mu.Lock()
	_, slot := room.slotOf(session)
	if slot != nil {
		slot.ready = true
	}
	step, mapID := room.step, room.mapID
	room.mu.Unlock()

	if step == proto.StepRace {
		session.Send(proto.SVEvent, proto.Event{Event: proto.EventMap, Data1: mapID, Data2: step}.Marshal())
		return
	}
	session.Send(proto.SVOK, nil)
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

	previous := slot.car
	slot.car = update.Car
	slot.joy = update.Joy

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
