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

// stateFrame builds the SV_FRAME broadcast, omitting the recipient's own slot:
// the client owns its local physics and only interpolates remote cars.
func (room *Room) stateFrame(exclude byte) proto.StateFrame {
	frame := proto.StateFrame{}
	for index, slot := range room.slots {
		if !slot.occupied() || byte(index) == exclude {
			continue
		}
		frame.Mask |= 1 << uint(index)
		frame.Cars[index] = slot.car
	}
	return frame
}

// broadcast sends one frame to every occupied slot.
func (room *Room) broadcast(opcode byte, payload []byte) {
	for _, slot := range room.slots {
		if slot.occupied() {
			slot.session.Send(opcode, payload)
		}
	}
}
