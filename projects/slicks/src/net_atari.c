#ifdef __ATARI__

#include "definitions.h"
#include "net_proto.h"
#include "ns_api.h"

#ifndef SLICKS_SERVER_HOST
	#define SLICKS_SERVER_HOST "192.168.1.120"
#endif
#ifndef SLICKS_SERVER_PORT
	#define SLICKS_SERVER_PORT 8320
#endif

/*
 * 31250 -- MIDI rate, matching the other working NetStream titles.
 *
 * This was pinned at 19200 on the theory that a MOTOR-only suspend is then
 * baud-transparent, because the Atari talks to the disk at
 * SIO_STANDARD_BAUDRATE. That was never true: FujiNet negotiates HSIO at
 * 57600 for disk loading, so the rates differed across a suspend no matter
 * what the stream ran at. The firmware now restores the SIO rate when MOTOR
 * de-asserts, which is what actually makes the handover safe and frees the
 * stream to run as fast as the link allows.
 *
 * Any value in the handler's table works; see BaudTable in
 * unity/targets/atari/netstream.s. An entry that is not in it fails the
 * lookup and ns_init_netstream() returns an error.
 *
 * Needs ref/netstream-plan/fujinet-motor-suspend-baud.patch, or HSIO off.
 */
#ifndef SLICKS_BAUD
	#define SLICKS_BAUD 31250
#endif

/* REGISTER | TX external clock. UDP: the server keys sessions by token, so a
   changed source port after a suspend still lands on the same slot. */
#define NETSTREAM_FLAGS 0x06

/* ns_send_byte() is non-blocking and the TX ring is a hard 128 bytes, so every
   byte needs a retry loop. Bounded so a dead link cannot hang the frame. */
#define TX_RETRIES 2000

extern void StopMusic(void);

static unsigned char netOpen = 0;

static unsigned int swap16(unsigned int value)
{
	return (unsigned int)((value << 8) | (value >> 8));
}

unsigned char NetOpen(void)
{
	/* Idempotent. interface.c opens the stream once from MenuConnect() to fetch
	   the room list, then MenuLogin() calls ServerConnect() again before joining.
	   Re-issuing ENABLE NETSTREAM asserts CMD on a link the FujiNet has already
	   switched to raw streaming, which tears the socket down for good
	   (ref/netstream-plan/00-constraints.md section 1). Drop the stale RX bytes
	   and keep the stream we have. */
	if (netOpen) {
		NetReset();
		return 1;
	}

	/* RMT drives all four POKEY channels and AUDCTL. Channels 3 and 4 are the
	   NetStream bit clock, so music and the stream cannot coexist. Stopping it
	   here means no future call path can reintroduce it behind our back.
	   See ref/netstream-plan/00-constraints.md section 2. */
	StopMusic();

	NetReset();

	/* This now reflects DSTATS, so a NAKed or timed-out enable is reported here
	   rather than surfacing later as an unexplained room-list timeout. It still
	   cannot detect an unreachable *host*: the FujiNet acknowledges the command
	   before it knows whether anything answers at the far end, so the ClientJoin
	   timeout remains the detector for that. */
	if (ns_init_netstream(SLICKS_SERVER_HOST, NETSTREAM_FLAGS, SLICKS_BAUD,
	                      swap16(SLICKS_SERVER_PORT)) != 0) {
		return 0;
	}

	ns_begin_stream();
	netOpen = 1;
	return 1;
}

void NetClose(void)
{
	if (!netOpen) { return; }
	ns_end_stream();
	netOpen = 0;
}

/*
 * Suspend drops MOTOR only. The socket and netstreamActive survive, so GameInit()
 * can load the next map from disk and the stream picks up where it left off --
 * no re-issue of the $F0 enable command, no re-REGISTER.
 *
 * The wait matters: a CMD frame asserted while MOTOR is still high makes the
 * firmware tear the socket down for good (sio.cpp:374-378). Two VBLANKs is a
 * guess until measured on hardware. If map changes drop the connection
 * intermittently, lengthen it -- that is the first thing to try.
 */
void NetSuspend(void)
{
	unsigned char frames;

	if (!netOpen) { return; }
	ns_suspend();

	frames = 2;
	while (frames--) {
		unsigned char vcount = PEEK(0xD40B);
		while (PEEK(0xD40B) == vcount) { }
		while (PEEK(0xD40B) != vcount) { }
	}
}

void NetResume(void)
{
	if (!netOpen) { return; }
	ns_resume();

	/* Bytes that arrived mid-suspend are a truncated frame at best; the handler
	   cleared its ring, so drop our partial assembly to match. */
	NetReset();
}

void NetPutBytes(const unsigned char* data, unsigned char len)
{
	unsigned char i;
	unsigned int retry;

	for (i = 0; i < len; ++i) {
		retry = TX_RETRIES;
		while (ns_send_byte(data[i]) != 0) {
			if (--retry == 0) {
				++netTxStalls;
				return;
			}
		}
	}
}

unsigned char NetAvail(void)
{
	return ns_bytes_avail() != 0;
}

int NetGetByte(void)
{
	return ns_recv_byte();
}

unsigned char NetStatus(void)
{
	/* Bits 0x40 and 0x80 are framing and overrun. Slicks' DLI and VBI sprite
	   work steals a lot of cycles, so this is worth watching. */
	return ns_get_status();
}

#endif
