#ifdef __LYNX__

#include <serial.h>

#include "definitions.h"
#include "net_proto.h"

#ifndef SLICKS_SERVER_HOST
	#define SLICKS_SERVER_HOST "192.168.1.120"
#endif
#ifndef SLICKS_SERVER_PORT
	#define SLICKS_SERVER_PORT 8320
#endif

#define FUJI_DEVICE_ID           0x70
#define FUJICMD_ENABLE_NETSTREAM 0xF0
#define FUJICMD_ACK              0x06
#define FUJICMD_NAK              0x15

/* TCP | REGISTER. TCP because the Lynx has no suspend to survive -- assets come
   from cartridge, so there is no disk conflict and the connection is continuous. */
#define NETSTREAM_FLAGS 0x03

/* ComLynx is half-duplex: ser_put disables RX for the duration of each byte, so
   a send can fail transiently and must be retried rather than dropped. */
#define TX_RETRIES 2000

extern void StopMusic(void);

static unsigned char netOpen = 0;

/* The cc65 ComLynx driver ring is only 256 bytes and slicks' redraws are long,
   so it is drained eagerly into a larger app-side ring. */
#define RX_RING 512

static unsigned char rxRing[RX_RING];
static unsigned int rxHead = 0, rxTail = 0;

static unsigned char xor8(const unsigned char* buffer, unsigned char length)
{
	unsigned char value = 0;
	while (length != 0) {
		value ^= *buffer++;
		--length;
	}
	return value;
}

static unsigned char GetByteTimeout(unsigned char* value)
{
	char byte;
	unsigned int timeout = 30000;

	while (timeout-- != 0) {
		if (ser_get(&byte) == SER_ERR_OK) {
			*value = (unsigned char)byte;
			return 1;
		}
	}
	return 0;
}

static unsigned char WaitForAck(void)
{
	unsigned char value;

	while (GetByteTimeout(&value)) {
		if (value == FUJICMD_ACK) { return 1; }
		if (value == FUJICMD_NAK) { return 0; }
	}
	return 0;
}

/*
 * Command framing: device id, 16-bit big-endian length, payload, XOR8 checksum.
 * Note the port inside the payload is little-endian -- unlike the Atari, where
 * it goes in DAUX1/DAUX2 big-endian.
 *
 * This must be the LAST FujiNet command sent. Once netstreamActive is set the
 * firmware routes ComLynx exclusively to the stream, so all $70 AppKey and $71
 * network work has to finish first. Phase 5's login flow slots in ahead of this
 * call, not after it. See ref/netstream-plan/00-constraints.md section 5.
 */
static unsigned char EnableNetstream(void)
{
	static const unsigned char host[] = SLICKS_SERVER_HOST;
	unsigned char payload[4 + sizeof(host)];
	unsigned char length = 0;
	unsigned char i;

	payload[length++] = FUJICMD_ENABLE_NETSTREAM;
	payload[length++] = (unsigned char)(SLICKS_SERVER_PORT & 0xff);
	payload[length++] = (unsigned char)(SLICKS_SERVER_PORT >> 8);
	for (i = 0; i < sizeof(host); ++i) {
		payload[length++] = host[i];
	}
	payload[length++] = NETSTREAM_FLAGS;

	while (ser_put(FUJI_DEVICE_ID) != SER_ERR_OK) { }
	while (ser_put(0) != SER_ERR_OK) { }
	while (ser_put((char)length) != SER_ERR_OK) { }
	for (i = 0; i < length; ++i) {
		while (ser_put((char)payload[i]) != SER_ERR_OK) { }
	}
	while (ser_put((char)xor8(payload, length)) != SER_ERR_OK) { }

	/* FujiNet switches ComLynx to raw NetStream immediately after the command
	   ACK; there is no second command-completion ACK to consume. */
	return WaitForAck();
}

unsigned char NetOpen(void)
{
	const struct ser_params params = {
		SER_BAUD_62500, SER_BITS_8, SER_STOP_1, SER_PAR_ODD, SER_HS_NONE
	};

	/* Idempotent, for the same reason as the Atari: interface.c connects once in
	   MenuConnect() and again in MenuLogin(), and a second ENABLE NETSTREAM on a
	   live link is a teardown, not a no-op.
	   See ref/netstream-plan/00-constraints.md section 5. */
	if (netOpen) {
		NetReset();
		rxHead = 0;
		rxTail = 0;
		return 1;
	}

	StopMusic();
	NetReset();
	rxHead = 0;
	rxTail = 0;

	if (ser_install(lynx_comlynx_ser) != SER_ERR_OK) { return 0; }
	if (ser_open(&params) != SER_ERR_OK) { return 0; }
	if (!EnableNetstream()) { return 0; }

	netOpen = 1;
	return 1;
}

void NetClose(void)
{
	if (!netOpen) { return; }
	ser_close();
	ser_uninstall();
	netOpen = 0;
}

/* No-ops on Lynx: assets live in the cartridge, so there is no disk I/O to make
   room for and nothing to suspend. */
void NetSuspend(void) { }
void NetResume(void) { }

void NetPutBytes(const unsigned char* data, unsigned char len)
{
	unsigned char i;
	unsigned int retry;

	for (i = 0; i < len; ++i) {
		retry = TX_RETRIES;
		while (ser_put((char)data[i]) != SER_ERR_OK) {
			if (--retry == 0) {
				++netTxStalls;
				return;
			}
		}
	}
}

void NetPumpRX(void)
{
	char byte;
	unsigned int next;

	while (ser_get(&byte) == SER_ERR_OK) {
		next = rxHead + 1;
		if (next >= RX_RING) { next = 0; }
		if (next == rxTail) {
			/* Ring full: the app is not draining fast enough. Dropping here
			   costs one frame, which the codec resynchronises past. */
			++netRxErrors;
			return;
		}
		rxRing[rxHead] = (unsigned char)byte;
		rxHead = next;
	}
}

unsigned char NetAvail(void)
{
	NetPumpRX();
	return rxHead != rxTail;
}

int NetGetByte(void)
{
	unsigned char value;

	if (rxHead == rxTail) { return -1; }
	value = rxRing[rxTail++];
	if (rxTail >= RX_RING) { rxTail = 0; }
	return value;
}

unsigned char NetStatus(void)
{
	unsigned char status = 0;
	ser_status(&status);
	return status;
}

#endif
