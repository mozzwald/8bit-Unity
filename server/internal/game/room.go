// Package game holds the authoritative room state: slot assignment, the
// WARMUP -> RACE -> RESULTS machine, and the 20 Hz SV_FRAME fan-out.
package game

import (
	"sync"
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
	"github.com/mozzwald/slicks-server/internal/transport"
)

// MapCount matches mapList[] at projects/slicks/src/slicks.c:39.
const MapCount = 10

// DefaultLapGoal is the number of laps a race runs for.
const DefaultLapGoal = 3

// QuietGrace is how long a slot may say nothing before the server stops sending
// it state. It exists to avoid burying a client that has dropped MOTOR to load a
// map, which takes seconds -- not to police jitter.
//
// A real Atari does not send at a steady 20 Hz: its loop is busy, and captured
// gaps run to 475ms routinely with occasional spikes past 2s. At 750ms those
// stalls cut the client's feed off and back on, which looks exactly like the
// erratic movement reported. Well under the client's 10s ERR_TIMEOUT, and still
// well under a map load.
const QuietGrace = 3 * time.Second

// RoomReport is how often a room logs what the server believes each slot's car
// is doing. It answers "does the server actually have this player's position?"
// directly, instead of inferring it from what a client does or does not draw.
const RoomReport = 5 * time.Second

// LynxProbe bounds how long a Lynx can go unanswered. It is served on demand,
// but a Lynx whose frames are all arriving damaged would never be answered and
// so would never receive anything either -- deaf as well as unheard, with no
// way back. Probing occasionally keeps it able to see the race, and to time out
// honestly if the link really is gone.
const LynxProbe = time.Second

// ResultsHold is how long RESULTS is shown before the room returns to WARMUP.
const ResultsHold = 8 * time.Second

// Slot is one of a room's four player positions.
type Slot struct {
	session  *transport.Session
	token    uint32
	name     [proto.NameBytes]byte
	platform byte

	car   proto.Car
	joy   byte
	ready bool

	lapBest  uint16
	finished bool
	position byte

	// lapStart timestamps the last waypoint-0 crossing, so the next one can be
	// timed. navRejects counts CL_FRAMEs whose navigation fields could not have
	// followed the previous ones.
	lapStart   time.Time
	navRejects uint16

	// lastHeard is when this slot last said anything. An Atari goes silent for
	// several seconds while it drops MOTOR and loads a map off disk, and it
	// cannot receive during that window: FujiNet buffers what it can and throws
	// the rest away, then delivers the survivors as one burst that overruns the
	// client's 256-byte ring. So the fan-out waits until the client is talking.
	lastHeard time.Time

	// lastServed is when this slot was last sent a state frame. A Lynx is
	// normally answered when it speaks, so this only matters as the probe floor
	// below.
	lastServed time.Time

	// spoke marks the first CL_FRAME the server managed to decode from a slot.
	spoke bool

	// carSeq counts accepted CL_FRAMEs, and sentSeq records, per recipient, the
	// carSeq of each source last included in a frame sent to them.
	//
	// Repeating a position the recipient already applied is not neutral: their
	// local sim keeps extrapolating the remote car forward with its last
	// joystick, and every stale repeat writes a delta pulling it back to where
	// it was up to half a second ago -- the car rubber-bands. So a car is put
	// in the mask only for recipients that have not already had that exact
	// update; absent from the mask means "not corrected this round", which is
	// what the client's own physics is for.
	//
	// Per recipient rather than one shared "is fresh" flag because the Atari
	// and the Lynx are served on different clocks. A shared flag can only be
	// cleared on the tick, and the Lynx is answered between ticks: an update
	// that landed just before a tick would be cleared by that tick's Atari
	// fan-out and never reach a Lynx polling just after it. That drops the
	// Lynx to half rate whenever its poll phase sits behind the tick.
	carSeq  uint16
	sentSeq [proto.MaxSlots]uint16

	// pending holds frames a Lynx must not be written right now. See deferForLynx.
	pending []pendingFrame
}

// pendingFrame is one frame held back until its recipient prompts for it.
type pendingFrame struct {
	opcode  byte
	payload []byte
}

// maxPendingLynxFrames bounds the hold-back queue. It is a delivery window, not
// a buffer: a Lynx this far behind is not reading, and the newest frame matters
// more than the oldest.
const maxPendingLynxFrames = 8

// deferForLynx holds a frame back until the Lynx prompts for it, reporting
// whether it took ownership of the send.
//
// Only a Lynx defers. ComLynx is one wire shared by both directions, and
// FujiNet discards its own echo by reading back exactly as many bytes as it
// wrote -- so a frame written while the Lynx is transmitting swallows the
// Lynx's bytes, and both frames are damaged (see the ComLynx note in
// handleCarFrame). The server may therefore write only in reply to something
// the Lynx sent.
//
// SV_FRAME survives a collision: it is resent 50ms later with newer positions.
// Nothing else does. A lost SV_INFO costs the whole roster, and with it every
// remote car, because it is the frame that marks a slot as NET_CONTROL; a lost
// EVENT_RACE or EVENT_MAP leaves the Lynx in warmup or on the wrong track while
// everyone else moves on. None of them are retried. So they queue here and go
// out in the Lynx's next reply slot, at most one server tick away.
func deferForLynx(slot *Slot, opcode byte, payload []byte) bool {
	if slot.platform != proto.PlatformLynx {
		return false
	}
	// SV_INFO is a snapshot, not an event: only the newest is worth anything,
	// and delivering a superseded one first would tell the Lynx a player who
	// has already joined is not there. Overwrite in place rather than append,
	// which also keeps it ordered against the events around it.
	if opcode == proto.SVInfo {
		for index := range slot.pending {
			if slot.pending[index].opcode == proto.SVInfo {
				slot.pending[index].payload = payload
				return true
			}
		}
	}
	if len(slot.pending) >= maxPendingLynxFrames {
		slot.pending = slot.pending[1:]
	}
	slot.pending = append(slot.pending, pendingFrame{opcode: opcode, payload: payload})
	return true
}

// takePending pops the next held-back frame. One write per prompt: handing the
// wire back mid-burst is the collision this exists to avoid.
func (slot *Slot) takePending() (pendingFrame, bool) {
	if len(slot.pending) == 0 {
		return pendingFrame{}, false
	}
	next := slot.pending[0]
	slot.pending = slot.pending[1:]
	return next, true
}

func (slot *Slot) occupied() bool { return slot.session != nil }

// Room is one race lobby: four slots and a state machine.
type Room struct {
	mu sync.Mutex

	name  string
	index byte

	slots [proto.MaxSlots]*Slot

	step    byte
	mapID   byte
	lapGoal byte

	// stateUntil gates the timed RESULTS -> WARMUP transition, and raceUntil
	// bounds a race nobody finishes.
	stateUntil time.Time
	raceUntil  time.Time

	// lastReport paces the periodic state line.
	lastReport time.Time
}

func newRoom(index byte, name string) *Room {
	room := &Room{name: name, index: index, step: proto.StepWarmup, lapGoal: DefaultLapGoal}
	for i := range room.slots {
		room.slots[i] = &Slot{}
	}
	return room
}

// Name reports the room's display name, used to build SV_LIST.
func (room *Room) Name() string { return room.name }

// join places session into a slot. A CL_JOIN carrying a token this room already
// knows re-binds that slot rather than consuming a new one, which is what lets
// an Atari survive a NetStream suspend across a map change.
func (room *Room) join(session *transport.Session, request proto.Join) (byte, bool) {
	room.mu.Lock()
	defer room.mu.Unlock()

	if request.Ticket != 0 {
		for index, slot := range room.slots {
			if slot.occupied() && slot.token == request.Ticket {
				slot.session = session
				return byte(index), true
			}
		}
	}
	for index, slot := range room.slots {
		if slot.occupied() {
			continue
		}
		*slot = Slot{
			session:  session,
			token:    request.Ticket,
			name:     request.Name,
			platform: request.Platform,
			car:      proto.Car{Lap: -1},
		}
		return byte(index), true
	}
	return 0, false
}

// leave frees whichever slot holds session.
func (room *Room) leave(session *transport.Session) bool {
	room.mu.Lock()
	defer room.mu.Unlock()
	for _, slot := range room.slots {
		if slot.session == session {
			*slot = Slot{}
			return true
		}
	}
	return false
}

func (room *Room) slotOf(session *transport.Session) (byte, *Slot) {
	for index, slot := range room.slots {
		if slot.session == session {
			return byte(index), slot
		}
	}
	return 0, nil
}

// info builds the SV_INFO snapshot from the perspective of the given slot.
func (room *Room) info(slot byte) proto.Info {
	room.mu.Lock()
	defer room.mu.Unlock()
	return room.infoLocked(slot)
}

func (room *Room) infoLocked(slot byte) proto.Info {
	info := proto.Info{Slot: slot, Map: room.mapID, Step: room.step, LapGoal: room.lapGoal}
	for index, entry := range room.slots {
		if !entry.occupied() {
			info.Slots[index] = proto.SlotInfo{Control: proto.SlotEmpty}
			continue
		}
		info.Slots[index] = proto.SlotInfo{Control: proto.SlotTaken, Name: entry.name}
	}
	return info
}

// frameFor builds the SV_FRAME for one recipient and records what it contained,
// so the next call will not repeat it. The recipient's own slot is always
// omitted: the client owns its local physics and only interpolates remote cars.
//
// It mutates the recipient's sentSeq, so it must be called once per frame
// actually sent, and never speculatively. Room.mu must be held.
func (room *Room) frameFor(recipient byte) proto.StateFrame {
	frame := proto.StateFrame{}
	to := room.slots[recipient]
	for index, slot := range room.slots {
		if !slot.occupied() || byte(index) == recipient {
			continue
		}
		// carSeq starts at zero and only advances on an accepted CL_FRAME, so
		// a slot that has joined but never reported keeps its placeholder car
		// out of every mask. That placeholder is (0,0), which a client would
		// otherwise draw off the top-left corner of the track.
		if slot.carSeq == to.sentSeq[index] {
			continue
		}
		to.sentSeq[index] = slot.carSeq
		frame.Mask |= 1 << uint(index)
		frame.Cars[index] = slot.car
	}
	return frame
}

// broadcast sends one frame to every occupied slot.
func (room *Room) broadcast(opcode byte, payload []byte) {
	for _, slot := range room.slots {
		if !slot.occupied() {
			continue
		}
		if deferForLynx(slot, opcode, payload) {
			continue
		}
		slot.session.Send(opcode, payload)
	}
}
