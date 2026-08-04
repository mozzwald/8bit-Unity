package transport

import (
	"log"
	"net"
	"sync"
	"time"
)

// IdleTimeout must outlast an Atari map-change suspend, which drops MOTOR while
// GameInit loads the next bitmap and .nav from disk.
const IdleTimeout = 15 * time.Second

// Listener serves UDP and TCP on one port simultaneously.
type Listener struct {
	handler Handler

	udpConn *net.UDPConn
	tcpConn net.Listener

	mu       sync.Mutex
	sessions map[string]*Session
}

// Listen binds both sockets on endpoint and starts serving.
func Listen(endpoint string, handler Handler) (*Listener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	tcpConn, err := net.Listen("tcp", endpoint)
	if err != nil {
		udpConn.Close()
		return nil, err
	}
	listener := &Listener{
		handler:  handler,
		udpConn:  udpConn,
		tcpConn:  tcpConn,
		sessions: make(map[string]*Session),
	}
	go listener.serveUDP()
	go listener.serveTCP()
	go listener.reapIdle()
	return listener, nil
}

// Close stops both listeners and drops every session.
func (listener *Listener) Close() {
	listener.udpConn.Close()
	listener.tcpConn.Close()
	for _, session := range listener.snapshot() {
		listener.drop(session)
	}
}

func (listener *Listener) snapshot() []*Session {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	sessions := make([]*Session, 0, len(listener.sessions))
	for _, session := range listener.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

func (listener *Listener) drop(session *Session) {
	key := session.Addr()
	listener.mu.Lock()
	if listener.sessions[key] == session {
		delete(listener.sessions, key)
	}
	listener.mu.Unlock()
	session.Close()
	listener.handler.OnClose(session)
}

// reapIdle closes sessions that have gone quiet, freeing their room slots.
func (listener *Listener) reapIdle() {
	ticker := time.NewTicker(IdleTimeout / 3)
	defer ticker.Stop()
	for range ticker.C {
		for _, session := range listener.snapshot() {
			if session.Idle() > IdleTimeout {
				log.Printf("%s: idle timeout", session.Addr())
				listener.drop(session)
			}
		}
	}
}

// session returns the existing session for key, or creates one over current.
func (listener *Listener) session(current link) (*Session, bool) {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if existing, ok := listener.sessions[current.key()]; ok {
		return existing, false
	}
	created := &Session{link: current, lastSeen: time.Now()}
	listener.sessions[current.key()] = created
	return created, true
}

type udpLink struct {
	conn *net.UDPConn
	addr *net.UDPAddr
}

func (l *udpLink) key() string { return "udp://" + l.addr.String() }
func (l *udpLink) write(data []byte) error {
	_, err := l.conn.WriteToUDP(data, l.addr)
	return err
}

// close is a no-op: the UDP socket is shared by every peer.
func (l *udpLink) close() {}

func (listener *Listener) serveUDP() {
	buf := make([]byte, readBuffer)
	seen := make(map[string]bool)
	for {
		n, addr, err := listener.udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		current := &udpLink{conn: listener.udpConn, addr: addr}
		session, created := listener.session(current)
		if created {
			log.Printf("%s: connected", current.key())
		} else {
			// Datagrams from a known peer may arrive on a fresh *net.UDPAddr.
			session.rebind(current)
		}
		data := append([]byte(nil), buf[:n]...)
		if !seen[current.key()] {
			seen[current.key()] = true
			data = stripRegister(data)
		}
		if len(data) != 0 {
			session.feed(data, listener.handler)
		}
	}
}

type tcpLink struct {
	conn net.Conn
}

func (l *tcpLink) key() string          { return "tcp://" + l.conn.RemoteAddr().String() }
func (l *tcpLink) write(d []byte) error { _, err := l.conn.Write(d); return err }
func (l *tcpLink) close()               { l.conn.Close() }

func (listener *Listener) serveTCP() {
	for {
		conn, err := listener.tcpConn.Accept()
		if err != nil {
			return
		}
		go listener.readTCP(&tcpLink{conn: conn})
	}
}

func (listener *Listener) readTCP(current *tcpLink) {
	session, _ := listener.session(current)
	log.Printf("%s: connected", current.key())
	defer listener.drop(session)

	buf := make([]byte, readBuffer)
	first := true
	for {
		n, err := current.conn.Read(buf)
		if err != nil {
			return
		}
		data := append([]byte(nil), buf[:n]...)
		if first {
			data = stripRegister(data)
			first = false
		}
		if len(data) != 0 {
			session.feed(data, listener.handler)
		}
	}
}
