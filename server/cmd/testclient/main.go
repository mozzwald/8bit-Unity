// Command testclient is a headless slicks-net client. Run several against
// gameserver to exercise a room with no 8-bit hardware in the loop.
package main

import (
	"flag"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
)

// client wraps one link and its inbound COBS decoder.
type client struct {
	conn     net.Conn
	decoder  proto.Decoder
	sequence byte
	slot     byte
	joined   bool
}

func (c *client) send(opcode byte, payload []byte) error {
	c.sequence++
	wire, err := proto.Encode(proto.Frame{Opcode: opcode, Seq: c.sequence, Payload: payload})
	if err != nil {
		return err
	}
	_, err = c.conn.Write(wire)
	return err
}

// receive pumps the socket and reports every decoded frame.
func (c *client) receive(frames chan<- proto.Frame) {
	buf := make([]byte, 2048)
	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			close(frames)
			return
		}
		decoded, errs := c.decoder.Push(buf[:n])
		for _, err := range errs {
			log.Printf("decode: %v", err)
		}
		for _, frame := range decoded {
			frames <- frame
		}
	}
}

func main() {
	host := flag.String("host", "127.0.0.1", "server host")
	port := flag.Int("port", 8320, "server port")
	network := flag.String("net", "udp", "transport: udp (Atari) or tcp (Lynx)")
	name := flag.String("name", "TESTER", "player name, up to 8 characters")
	room := flag.Int("room", 0, "room index to join")
	ticket := flag.Uint("ticket", 0, "rejoin token; 0 requests a fresh slot")
	rate := flag.Int("rate", 20, "CL_FRAME send rate in Hz")
	list := flag.Bool("list", false, "request the room list and exit")
	flag.Parse()

	platform := byte(proto.PlatformAtari)
	if *network == "tcp" {
		platform = proto.PlatformLynx
	}

	conn, err := net.Dial(*network, net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	c := &client{conn: conn}

	frames := make(chan proto.Frame, 64)
	go c.receive(frames)

	if *list {
		if err := c.send(proto.CLList, nil); err != nil {
			log.Fatal(err)
		}
		printList(frames)
		return
	}

	join := proto.Join{
		Ticket:   uint32(*ticket),
		Platform: platform,
		Version:  proto.ProtocolVersion,
		Room:     byte(*room),
	}
	copy(join.Name[:], *name)
	if err := c.send(proto.CLJoin, join.Marshal()); err != nil {
		log.Fatal(err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(time.Second / time.Duration(*rate))
	defer ticker.Stop()
	start := time.Now()

	for {
		select {
		case <-signals:
			c.send(proto.CLLeave, nil)
			return
		case frame, ok := <-frames:
			if !ok {
				log.Print("link closed")
				return
			}
			c.handle(frame)
		case <-ticker.C:
			if !c.joined {
				continue
			}
			// Drive in a circle so remote interpolation has something to chew on.
			angle := time.Since(start).Seconds()
			car := proto.Car{
				X:   int16(1000 + 400*math.Cos(angle)),
				Y:   int16(1000 + 400*math.Sin(angle)),
				Ang: int16(int(angle*180/math.Pi) % 360),
				Vel: 64,
				Way: byte(int(angle) % 8),
				Lap: 0,
			}
			c.send(proto.CLFrame, proto.CarFrame{Car: car}.Marshal())
		}
	}
}

func (c *client) handle(frame proto.Frame) {
	switch frame.Opcode {
	case proto.SVInfo:
		info, err := proto.ParseInfo(frame.Payload)
		if err != nil {
			log.Printf("bad SV_INFO: %v", err)
			return
		}
		if !c.joined {
			c.joined = true
			c.slot = info.Slot
			log.Printf("joined as slot %d, map %d, step %d, %d laps",
				info.Slot, info.Map, info.Step, info.LapGoal)
			c.send(proto.CLReady, nil)
		}
		for index, slot := range info.Slots {
			if slot.Control != proto.SlotEmpty {
				log.Printf("  slot %d: %s", index, proto.Trim(slot.Name))
			}
		}
	case proto.SVFrame:
		state, err := proto.ParseStateFrame(frame.Payload)
		if err != nil {
			log.Printf("bad SV_FRAME: %v", err)
		}
		_ = state
	case proto.SVEvent:
		event, err := proto.ParseEvent(frame.Payload)
		if err != nil {
			return
		}
		log.Printf("event %d from slot %d (%d,%d) %q",
			event.Event, event.Slot, event.Data1, event.Data2, event.Text)
	case proto.SVOK:
		log.Print("ready acknowledged")
	case proto.SVError:
		log.Printf("server error: %s", trimNUL(frame.Payload))
	}
}

func printList(frames <-chan proto.Frame) {
	deadline := time.After(2 * time.Second)
	seen := 0
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if frame.Opcode != proto.SVList {
				continue
			}
			entry, err := proto.ParseListEntry(frame.Payload)
			if err != nil {
				log.Printf("bad SV_LIST: %v", err)
				continue
			}
			log.Printf("room %d: %s", entry.Index, entry.Name)
			if seen++; seen >= int(entry.Total) {
				return
			}
		case <-deadline:
			log.Print("timed out waiting for room list")
			return
		}
	}
}

func trimNUL(data []byte) string {
	for index, value := range data {
		if value == 0 {
			return string(data[:index])
		}
	}
	return string(data)
}
