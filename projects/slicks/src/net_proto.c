#include "definitions.h"
#include "net_proto.h"

/* The codec's buffers live in main RAM rather than the RMT player region that
   holds the rest of BSS. That region is 256 bytes short of the whole of BSS, and
   main RAM has the slack; this file is the natural place to take it from, being
   NetStream-only and self-contained.
   See unity/targets/atari/atarixl-netstream.cfg. */
#if defined(__ATARI__) && defined(__NETSTREAM__)
  #pragma bss-name(push, "NETBSS2")
#endif

NetFrame netFrame;

unsigned int  netTxStalls = 0;
unsigned char netRxErrors = 0;

/* Assembly buffer for the COBS-encoded bytes between two 0x00 delimiters. */
static unsigned char rxWire[NET_MAX_WIRE];
static unsigned char rxLen = 0;

/* Scratch for encode and decode. Separate from rxWire so a send can happen
   while a partial frame is still being assembled. */
static unsigned char raw[NET_MAX_RAW];
static unsigned char wire[NET_MAX_WIRE + 1];

#if defined(__ATARI__) && defined(__NETSTREAM__)
  #pragma bss-name(pop)
#endif

unsigned int NetCRC16(const unsigned char* data, unsigned char len)
{
	unsigned int crc = 0xffff;
	unsigned char i, bit;

	/* The masks are no-ops under cc65, where int is exactly 16 bits, but the
	   algorithm depends on 16-bit wraparound and this file is also compiled
	   natively by the conformance test. */
	for (i = 0; i < len; ++i) {
		crc ^= ((unsigned int)data[i]) << 8;
		crc &= 0xffff;
		for (bit = 0; bit < 8; ++bit) {
			if (crc & 0x8000) {
				crc = ((crc << 1) ^ 0x1021) & 0xffff;
			} else {
				crc = (crc << 1) & 0xffff;
			}
		}
	}
	return crc & 0xffff;
}

void NetReset(void)
{
	rxLen = 0;
	netRxErrors = 0;
	netTxStalls = 0;
}

/* COBS-encode raw[0..len-1] into wire[], returning the encoded length. */
static unsigned char CobsEncode(unsigned char len)
{
	unsigned char in = 0, out = 1, code = 1, codeAt = 0;

	while (in < len) {
		if (raw[in] == 0) {
			wire[codeAt] = code;
			codeAt = out++;
			code = 1;
		} else {
			wire[out++] = raw[in];
			++code;
		}
		++in;
	}
	wire[codeAt] = code;
	return out;
}

/* COBS-decode rxWire[0..len-1] into raw[]. Returns the decoded length, or 0. */
static unsigned char CobsDecode(unsigned char len)
{
	unsigned char in = 0, out = 0, code, i;

	while (in < len) {
		code = rxWire[in];
		if (code == 0) { return 0; }
		++in;
		for (i = 1; i < code; ++i) {
			if (in >= len || out >= NET_MAX_RAW) { return 0; }
			raw[out++] = rxWire[in++];
		}
		if (code != 0xff && in < len) {
			if (out >= NET_MAX_RAW) { return 0; }
			raw[out++] = 0;
		}
	}
	return out;
}

void NetSend(unsigned char opcode, const unsigned char* payload, unsigned char len)
{
	unsigned int crc;
	unsigned char i, size;
	static unsigned char seq = 0;

	if (len > NET_MAX_PAYLOAD) { return; }

	raw[0] = opcode;
	raw[1] = ++seq;
	raw[2] = len;
	for (i = 0; i < len; ++i) { raw[3 + i] = payload[i]; }

	crc = NetCRC16(raw, 3 + len);
	raw[3 + len] = (unsigned char)crc;
	raw[4 + len] = (unsigned char)(crc >> 8);

	size = CobsEncode(5 + len);
	wire[size++] = 0;
	NetPutBytes(wire, size);
}

/* Validates the frame currently assembled in rxWire and copies it to netFrame. */
static unsigned char NetDecodeWire(void)
{
	unsigned int want, have;
	unsigned char len, i;

	len = CobsDecode(rxLen);
	if (len < 5 || raw[2] > NET_MAX_PAYLOAD || raw[2] + 5 != len) {
		++netRxErrors;
		return 0;
	}

	want = ((unsigned int)raw[len - 1] << 8) | raw[len - 2];
	have = NetCRC16(raw, len - 2);
	if (want != have) {
		++netRxErrors;
		return 0;
	}

	netFrame.opcode = raw[0];
	netFrame.seq = raw[1];
	netFrame.len = raw[2];
	for (i = 0; i < netFrame.len; ++i) { netFrame.payload[i] = raw[3 + i]; }
	return 1;
}

unsigned char NetPoll(void)
{
	int value;

	while (NetAvail() != 0) {
		value = NetGetByte();
		if (value < 0) { break; }

		if (value != 0) {
			/* Overlong runs mean a lost delimiter; drop and resynchronise. */
			if (rxLen >= NET_MAX_WIRE) {
				rxLen = 0;
				++netRxErrors;
				continue;
			}
			rxWire[rxLen++] = (unsigned char)value;
			continue;
		}

		if (rxLen == 0) { continue; }
		value = NetDecodeWire();
		rxLen = 0;
		if (value) { return 1; }
	}
	return 0;
}

unsigned char NetWait(unsigned int ticks)
{
	clock_t deadline = clock() + ticks;

	while (clock() < deadline) {
		if (NetPoll()) { return 1; }
	}
	return 0;
}
