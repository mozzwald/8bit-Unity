package game

import (
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
)

// TicksPerSecond is the unit the client prints lap times in: PrintBestLap()
// (interface.c:474) divides by its own TCK_PER_SEC, which is 60 on NTSC Atari
// and on Lynx. A PAL Atari would divide by 50 and read times ~20% short; that
// matches the NTSC assumption already made for the protocol timeouts in
// projects/slicks/src/network.c.
const TicksPerSecond = 60

// RaceTimeout ends a race that nobody finishes, so a room cannot be wedged in
// RACE by a player who parks on the grid.
const RaceTimeout = 10 * time.Minute

// wayCount is how many half-waypoint steps make up one lap of the current map.
//
// game.c:826-832 advances Vehicle.way by one per waypoint crossing and wraps it
// with `if (way/2 == numWays) way = 0`, so a lap is numWays*2 steps and the lap
// counter increments on the 0 -> 1 transition.
func (room *Room) wayCount() byte {
	if int(room.mapID) >= len(Tracks) {
		return 0
	}
	return Tracks[room.mapID].NumWays * 2
}

// nextWay reports the only waypoint value that may legally follow current.
func nextWay(current, span byte) byte {
	if span == 0 {
		return current
	}
	if current+1 >= span {
		return 0
	}
	return current + 1
}

// lapProgress is what the server decides about one CL_FRAME's navigation fields.
type lapProgress struct {
	valid     bool   // the way/lap pair is a legal successor of the previous one
	completed bool   // this frame closed a timed lap
	lapTicks  uint16 // lap duration, in client ticks; only set when completed
	finished  bool   // this frame took the player to lapGoal
}

// trackLapLocked validates one player's navigation progress and, when a lap has
// legitimately been closed, times it. Room.mu must be held.
//
// The client owns its own physics, so these fields are self-reported; all the
// server can do is reject sequences the game could not have produced. A frame
// that fails validation leaves the slot's lap accounting untouched, so skipping
// waypoints or teleporting to the finish line credits nothing.
func (room *Room) trackLapLocked(slot *Slot, prev, next proto.Car, now time.Time) lapProgress {
	span := room.wayCount()
	if span == 0 {
		return lapProgress{}
	}

	// Standing still between frames is normal at 20 Hz.
	if next.Way == prev.Way && next.Lap == prev.Lap {
		return lapProgress{valid: true}
	}

	expected := nextWay(prev.Way, span)
	if next.Way != expected {
		slot.navRejects++
		return lapProgress{}
	}

	// The lap counter may only move on the 0 -> 1 wrap, and only by one.
	wrapped := expected == 1
	if wrapped {
		if next.Lap != prev.Lap+1 {
			slot.navRejects++
			return lapProgress{}
		}
	} else if next.Lap != prev.Lap {
		slot.navRejects++
		return lapProgress{}
	}

	result := lapProgress{valid: true}
	if !wrapped {
		return result
	}

	// Lap 0 is the start-line crossing that opens the first timed lap; only
	// later crossings close one (game.c:834).
	if next.Lap <= 0 {
		slot.lapStart = now
		return result
	}

	if !slot.lapStart.IsZero() {
		result.completed = true
		result.lapTicks = ticksBetween(slot.lapStart, now)
		if slot.lapBest == 0 || result.lapTicks < slot.lapBest {
			slot.lapBest = result.lapTicks
		}
	}
	slot.lapStart = now

	if byte(next.Lap) >= room.lapGoal {
		result.finished = true
	}
	return result
}

// ticksBetween converts elapsed wall time to client ticks, saturating rather
// than wrapping: lapBest is a uint16 the client compares against LAPMAX.
func ticksBetween(start, end time.Time) uint16 {
	elapsed := end.Sub(start)
	if elapsed <= 0 {
		return 0
	}
	ticks := elapsed.Seconds() * TicksPerSecond
	if ticks >= 0xFFFF {
		return 0xFFFF
	}
	return uint16(ticks)
}

// finishLocked records a player's finishing position. Room.mu must be held.
func (room *Room) finishLocked(slot *Slot) {
	if slot.finished {
		return
	}
	slot.finished = true
	position := byte(1)
	for _, other := range room.slots {
		if other.occupied() && other.finished && other != slot {
			position++
		}
	}
	slot.position = position
}

// raceOverLocked reports whether every racer has finished. Room.mu must be held.
func (room *Room) raceOverLocked() bool {
	racing := 0
	for _, slot := range room.slots {
		if !slot.occupied() {
			continue
		}
		if !slot.finished {
			return false
		}
		racing++
	}
	return racing > 0
}

// enterResultsLocked moves the room out of RACE. Room.mu must be held.
//
// No event is broadcast here: the clients stay in their race loop showing the
// final positions until the RESULTS hold expires and tickRoom sends EVENT_MAP,
// which is what drives PrintScores() and the map change at game.c:1006.
func (room *Room) enterResultsLocked(now time.Time) {
	room.step = stepResults
	room.stateUntil = now.Add(ResultsHold)
}
