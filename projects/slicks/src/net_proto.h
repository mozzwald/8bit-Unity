#ifndef SLICKS_NET_PROTO_H
#define SLICKS_NET_PROTO_H

/*
 * slicks-net v1 codec, shared by every NetStream target.
 *
 * Wire format, matching server/internal/proto byte for byte:
 *
 *   raw    [0] opcode  [1] seq  [2] len  [3..] payload  [..] CRC-16 LE
 *   stream COBS(raw) followed by a 0x00 delimiter
 *
 * See ref/netstream-plan/01-protocol.md.
 */

#define NET_MAX_PAYLOAD 48
#define NET_MAX_RAW     (3 + NET_MAX_PAYLOAD + 2)

/* Worst-case COBS expansion is one overhead byte per 254, plus the leading code. */
#define NET_MAX_WIRE    (NET_MAX_RAW + 2)

/* Protocol version carried in CL_JOIN. */
#define NET_VERSION 1

/* CL_JOIN platform byte. */
#if defined(__LYNX__)
	#define NET_PLATFORM 2
#else
	#define NET_PLATFORM 1
#endif

/* SV_INFO per-slot occupancy. Not NET_CONTROL: that is LEN_CONTROL-1 and varies
   by target, so the value would be meaningless across the wire. */
#define NET_SLOT_EMPTY 0
#define NET_SLOT_TAKEN 1

/* One decoded frame. */
typedef struct {
	unsigned char opcode;
	unsigned char seq;
	unsigned char len;
	unsigned char payload[NET_MAX_PAYLOAD];
} NetFrame;

/* The most recently decoded frame, valid until the next NetPoll(). */
extern NetFrame netFrame;

/* Link error counters, surfaced by the debug build. */
extern unsigned int netTxStalls;
extern unsigned char netRxErrors;

/*
 * Platform transport, implemented by net_atari.c and net_lynx.c.
 */
unsigned char NetOpen(void);
void NetClose(void);
void NetSuspend(void);
void NetResume(void);
void NetPutBytes(const unsigned char* data, unsigned char len);
unsigned char NetAvail(void);
int  NetGetByte(void);
unsigned char NetStatus(void);

/* Lynx only: drains the small ComLynx driver ring into the app-side ring.
   Called from the display wait and the pause loop as well as NetAvail(). */
#ifdef __LYNX__
void NetPumpRX(void);
#else
	#define NetPumpRX()
#endif

/*
 * Codec.
 */
void NetReset(void);
void NetSend(unsigned char opcode, const unsigned char* payload, unsigned char len);

/* Reads one frame into netFrame. Returns 1 when a frame is ready, else 0. */
unsigned char NetPoll(void);

/* Blocks up to `ticks` waiting for any frame. Returns 1 with netFrame filled in,
   or 0 on timeout. Callers switch on netFrame.opcode themselves, because a
   handshake reply can legitimately arrive as SV_ERROR instead. */
unsigned char NetWait(unsigned int ticks);

unsigned int NetCRC16(const unsigned char* data, unsigned char len);

#endif
