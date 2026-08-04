// Command echoserver provides the Phase 0 NetStream interoperability target.
// It accepts FujiNet UDP and TCP raw streams on one port, consumes the optional
// REGISTER preamble, and broadcasts subsequent bytes to every other client.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"sync"
)

const registerPreamble = "REGISTER"

type peer interface {
	id() string
	write([]byte) error
}

type udpPeer struct {
	conn *net.UDPConn
	addr *net.UDPAddr
}

func (p *udpPeer) id() string { return "udp://" + p.addr.String() }
func (p *udpPeer) write(data []byte) error {
	_, err := p.conn.WriteToUDP(data, p.addr)
	return err
}

type tcpPeer struct {
	conn net.Conn
}

func (p *tcpPeer) id() string { return "tcp://" + p.conn.RemoteAddr().String() }
func (p *tcpPeer) write(data []byte) error {
	_, err := p.conn.Write(data)
	return err
}

type hub struct {
	mu    sync.RWMutex
	peers map[string]peer
}

func newHub() *hub { return &hub{peers: make(map[string]peer)} }

func (h *hub) add(p peer) {
	h.mu.Lock()
	h.peers[p.id()] = p
	h.mu.Unlock()
	log.Printf("connected %s", p.id())
}

func (h *hub) remove(id string) {
	h.mu.Lock()
	if _, ok := h.peers[id]; ok {
		delete(h.peers, id)
		log.Printf("disconnected %s", id)
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(sender string, data []byte) {
	log.Printf("received %d bytes from %s", len(data), sender)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, p := range h.peers {
		if id == sender {
			continue
		}
		if err := p.write(data); err != nil {
			log.Printf("write %s: %v", id, err)
		}
	}
}

func stripRegister(data []byte) []byte {
	if len(data) >= len(registerPreamble) && string(data[:len(registerPreamble)]) == registerPreamble {
		return data[len(registerPreamble):]
	}
	return data
}

func serveUDP(conn *net.UDPConn, h *hub) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("udp read: %v", err)
			return
		}
		p := &udpPeer{conn: conn, addr: addr}
		h.add(p)
		data := stripRegister(append([]byte(nil), buf[:n]...))
		if len(data) != 0 {
			h.broadcast(p.id(), data)
		}
	}
}

func serveTCP(listener net.Listener, h *hub) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("tcp accept: %v", err)
			return
		}
		go func() {
			p := &tcpPeer{conn: conn}
			h.add(p)
			defer func() {
				h.remove(p.id())
				conn.Close()
			}()
			buf := make([]byte, 2048)
			first := true
			for {
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				data := append([]byte(nil), buf[:n]...)
				if first {
					data = stripRegister(data)
					first = false
				}
				if len(data) != 0 {
					h.broadcast(p.id(), data)
				}
			}
		}()
	}
}

func main() {
	addr := flag.String("addr", "0.0.0.0", "listen address")
	port := flag.Int("port", 8320, "UDP and TCP listen port")
	flag.Parse()

	endpoint := net.JoinHostPort(*addr, fmt.Sprint(*port))
	udpAddr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		log.Fatal(err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer udpConn.Close()
	tcpListener, err := net.Listen("tcp", endpoint)
	if err != nil {
		log.Fatal(err)
	}
	defer tcpListener.Close()

	log.Printf("NetStream echo listening on udp://%s and tcp://%s", endpoint, endpoint)
	h := newHub()
	go serveUDP(udpConn, h)
	serveTCP(tcpListener, h)
}
