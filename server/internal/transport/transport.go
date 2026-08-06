// Package transport carries slicks-net frames over the two NetStream link
// types: UDP for the Atari 8-bit and TCP for the Lynx. Both feed one Session
// abstraction so internal/game never sees the difference.
package transport

import (
	"encoding/hex"
	"log"
	"net"
	"sync"
	"time"

	"github.com/mozzwald/slicks-server/internal/proto"
)

// registerPreamble is the FujiNet NetStream REGISTER greeting, emitted once by
// clients that set the REGISTER flag. It is not part of slicks-net.
const registerPreamble = "REGISTER"

// readBuffer is generous: a UDP datagram may batch several frames.
const readBuffer = 2048

// Session is one client link. It is created on first contact and outlives any
// individual UDP source address, so an Atari that suspends its stream for a map
// change and resumes on a new source port keeps its slot (see
// ref/netstream-plan/00-constraints.md section 1).
type Session struct {
	mu       sync.Mutex
	link     link
	decoder  proto.Decoder
	sequence byte
	lastSeen time.Time
	lastDump time.Time
	closed   bool
}

// link is the socket-level write path behind a Session.
type link interface {
	key() string
	write([]byte) error
	close()
}

// Handler receives decoded frames and lifecycle notifications. All callbacks
// run on the transport's goroutines and must not block for long.
type Handler interface {
	OnFrame(*Session, proto.Frame)
	OnClose(*Session)
}

// Addr reports the current peer address, for logging.
func (session *Session) Addr() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.link.key()
}

// Send encodes and writes one frame, assigning the next rolling sequence number.
func (session *Session) Send(opcode byte, payload []byte) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return net.ErrClosed
	}
	session.sequence++
	frame := proto.Frame{Opcode: opcode, Seq: session.sequence, Payload: payload}
	current := session.link
	session.mu.Unlock()

	wire, err := proto.Encode(frame)
	if err != nil {
		return err
	}
	return current.write(wire)
}

// Close tears the session down. It is safe to call more than once.
func (session *Session) Close() {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return
	}
	session.closed = true
	current := session.link
	session.mu.Unlock()
	current.close()
}

// Idle reports how long since the last inbound byte.
func (session *Session) Idle() time.Duration {
	session.mu.Lock()
	defer session.mu.Unlock()
	return time.Since(session.lastSeen)
}

// rebind repoints a session at a new socket, used when a UDP peer's source
// address changes across a NetStream suspend/resume.
func (session *Session) rebind(current link) {
	session.mu.Lock()
	session.link = current
	session.mu.Unlock()
}

// feed decodes inbound bytes and dispatches whole frames to the handler.
func (session *Session) feed(data []byte, handler Handler) {
	session.mu.Lock()
	session.lastSeen = time.Now()
	frames, errs := session.decoder.Push(data)
	session.mu.Unlock()

	for _, err := range errs {
		log.Printf("%s: %v", session.Addr(), err)
	}
	if len(errs) != 0 {
		// A decode error says the bytes were damaged but not how. Dump the chunk
		// that produced it, throttled, so the shape is visible: echoed server
		// frames look like SV_ opcodes coming back, a half-duplex collision
		// looks like plausible bytes with a bad CRC, and a truncated write ends
		// mid-frame with no delimiter.
		session.mu.Lock()
		show := time.Since(session.lastDump) > time.Second
		if show {
			session.lastDump = time.Now()
		}
		session.mu.Unlock()
		if show {
			log.Printf("%s: raw in (%d bytes): %s", session.Addr(), len(data), hex.EncodeToString(data))
		}
	}
	for _, frame := range frames {
		handler.OnFrame(session, frame)
	}
}

func stripRegister(data []byte) []byte {
	if len(data) >= len(registerPreamble) && string(data[:len(registerPreamble)]) == registerPreamble {
		return data[len(registerPreamble):]
	}
	return data
}
