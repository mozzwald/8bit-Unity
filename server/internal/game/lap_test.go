package game

import (
	"testing"
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
	"github.com/mozzwald/slicks-server/internal/transport"
)

// arizona is map 0 with 6 waypoints, so a lap is 12 half-steps.
const arizonaSpan = 12

// fakeSession only has to be a non-nil pointer: these tests call the locked
// helpers directly, so nothing is ever written to it.
var fakeSession transport.Session

func raceRoom(t *testing.T) (*Room, *Slot) {
	t.Helper()
	room := newRoom(0, "Test")
	room.step = proto.StepRace
	room.mapID = 0
	slot := room.slots[0]
	slot.session = &fakeSession
	slot.car = proto.Car{Lap: -1}
	return room, slot
}

// drive walks the car one waypoint forward, incrementing the lap on the wrap.
func drive(car proto.Car) proto.Car {
	next := car
	next.Way = nextWay(car.Way, arizonaSpan)
	if next.Way == 1 {
		next.Lap++
	}
	return next
}

func TestWayCountMatchesNavData(t *testing.T) {
	room := newRoom(0, "Test")
	for id := range Tracks {
		room.mapID = byte(id)
		if got, want := room.wayCount(), Tracks[id].NumWays*2; got != want {
			t.Fatalf("%s: wayCount = %d, want %d", Tracks[id].Name, got, want)
		}
	}
}

func TestLapTimedOnSecondCrossing(t *testing.T) {
	room, slot := raceRoom(t)
	now := time.Unix(0, 0)
	car := proto.Car{Lap: -1}

	// First crossing of the line opens lap 0 and must not be timed.
	for step := 0; step < arizonaSpan; step++ {
		next := drive(car)
		progress := room.trackLapLocked(slot, car, next, now)
		if !progress.valid {
			t.Fatalf("step %d rejected a legal advance", step)
		}
		if progress.completed {
			t.Fatalf("step %d timed a lap before the start line", step)
		}
		car = next
		now = now.Add(100 * time.Millisecond)
	}
	if car.Lap != 0 {
		t.Fatalf("after one circuit Lap = %d, want 0", car.Lap)
	}

	// The line is crossed at the *start* of each circuit, so the next crossing
	// closes the first timed lap: twelve steps at 100ms is 1.2s.
	for step := 0; step < arizonaSpan; step++ {
		next := drive(car)
		progress := room.trackLapLocked(slot, car, next, now)
		if !progress.valid {
			t.Fatalf("second circuit step %d rejected", step)
		}
		if step == 0 {
			if !progress.completed {
				t.Fatal("crossing the line a second time did not time a lap")
			}
			want := uint16(1.2 * TicksPerSecond)
			if progress.lapTicks != want {
				t.Fatalf("lapTicks = %d, want %d", progress.lapTicks, want)
			}
			if slot.lapBest != want {
				t.Fatalf("lapBest = %d, want %d", slot.lapBest, want)
			}
		} else if progress.completed {
			t.Fatalf("second circuit step %d timed a lap mid-circuit", step)
		}
		car = next
		now = now.Add(100 * time.Millisecond)
	}
}

func TestSkippedWaypointIsRejected(t *testing.T) {
	room, slot := raceRoom(t)
	now := time.Unix(0, 0)
	car := proto.Car{Way: 3, Lap: 0}
	slot.car = car

	jump := car
	jump.Way = 7 // skipped 4, 5 and 6

	progress := room.trackLapLocked(slot, car, jump, now)
	if progress.valid {
		t.Fatal("a four-waypoint jump was accepted")
	}
	if slot.navRejects != 1 {
		t.Fatalf("navRejects = %d, want 1", slot.navRejects)
	}
}

func TestLapCannotBeClaimedWithoutTheCircuit(t *testing.T) {
	room, slot := raceRoom(t)
	now := time.Unix(0, 0)
	car := proto.Car{Way: 4, Lap: 0}
	slot.car = car

	// Claim a lap without returning to the line.
	cheat := car
	cheat.Way = 5
	cheat.Lap = 5

	progress := room.trackLapLocked(slot, car, cheat, now)
	if progress.valid || progress.completed || progress.finished {
		t.Fatalf("a fabricated lap counter was accepted: %+v", progress)
	}
}

func TestStationaryFrameIsValidAndUntimed(t *testing.T) {
	room, slot := raceRoom(t)
	car := proto.Car{Way: 2, Lap: 1}
	slot.car = car

	progress := room.trackLapLocked(slot, car, car, time.Unix(0, 0))
	if !progress.valid {
		t.Fatal("an unchanged frame was rejected")
	}
	if progress.completed {
		t.Fatal("an unchanged frame timed a lap")
	}
}

func TestFinishAtLapGoalAndPositionOrder(t *testing.T) {
	room, _ := raceRoom(t)
	room.lapGoal = 2
	first, second := room.slots[0], room.slots[1]
	second.session = &fakeSession
	second.car = proto.Car{Lap: -1}

	now := time.Unix(0, 0)
	for _, entry := range []*Slot{first, second} {
		car := proto.Car{Lap: -1}
		finished := false
		// One crossing opens lap 0, so lapGoal+1 circuits reach the flag.
		for lap := 0; lap <= int(room.lapGoal); lap++ {
			for step := 0; step < arizonaSpan; step++ {
				next := drive(car)
				progress := room.trackLapLocked(entry, car, next, now)
				if progress.finished {
					finished = true
					room.finishLocked(entry)
				}
				car = next
				now = now.Add(50 * time.Millisecond)
			}
		}
		if !finished {
			t.Fatalf("reaching lapGoal %d did not finish the racer", room.lapGoal)
		}
	}

	if first.position != 1 || second.position != 2 {
		t.Fatalf("positions = %d, %d; want 1, 2", first.position, second.position)
	}
	if !room.raceOverLocked() {
		t.Fatal("race not over with both racers finished")
	}
}

func TestRaceNotOverWhileSomeoneIsStillRacing(t *testing.T) {
	room, first := raceRoom(t)
	second := room.slots[1]
	second.session = &fakeSession

	room.finishLocked(first)
	if room.raceOverLocked() {
		t.Fatal("race reported over while a racer is still going")
	}
}

func TestTicksBetweenSaturates(t *testing.T) {
	start := time.Unix(0, 0)
	if got := ticksBetween(start, start.Add(-time.Second)); got != 0 {
		t.Fatalf("negative interval = %d, want 0", got)
	}
	if got := ticksBetween(start, start.Add(time.Hour)); got != 0xFFFF {
		t.Fatalf("an hour = %d, want saturation at 0xFFFF", got)
	}
}
