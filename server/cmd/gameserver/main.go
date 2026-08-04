// Command gameserver runs the slicks-net v1 authoritative race server. It
// accepts FujiNet NetStream links over UDP (Atari 8-bit) and TCP (Lynx) on one
// port; see ref/netstream-plan/01-protocol.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/mozzwald/slicks-server/internal/game"
	"github.com/mozzwald/slicks-server/internal/transport"
)

func main() {
	addr := flag.String("addr", "0.0.0.0", "listen address")
	port := flag.Int("port", 8320, "UDP and TCP listen port")
	rooms := flag.Int("rooms", 4, "number of race rooms")
	tick := flag.Int("tick", 20, "server tick rate in Hz")
	flag.Parse()

	if *rooms < 1 || *rooms > 12 {
		log.Fatal("rooms must be 1..12; the client menu lists at most 12")
	}
	if *tick < 1 || *tick > 60 {
		log.Fatal("tick must be 1..60")
	}

	server := game.New(*rooms, *tick)
	endpoint := net.JoinHostPort(*addr, fmt.Sprint(*port))
	listener, err := transport.Listen(endpoint, server)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("slicks-net listening on udp://%s and tcp://%s, %d rooms at %d Hz",
		endpoint, endpoint, *rooms, *tick)
	go server.Run()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	log.Print("shutting down")
	server.Stop()
}
