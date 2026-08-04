/*
 * Host-side conformance test for the client codec.
 *
 * net_proto.c is the one piece of client code that has to agree byte for byte
 * with server/internal/proto. This compiles it natively and emits vectors that
 * TestClientVectors in server/internal/proto checks against the Go codec.
 *
 *   ./projects/slicks/build_netstream.sh test
 */
#include <stdio.h>
#include <string.h>

#include "net_proto.c"

/* Loopback transport: NetSend writes here, NetPoll reads back. */
static unsigned char loop[4096];
static unsigned int loopLen = 0, loopRead = 0;

void NetPutBytes(const unsigned char* data, unsigned char len)
{
	unsigned char i;
	for (i = 0; i < len; ++i) { loop[loopLen++] = data[i]; }
}

unsigned char NetAvail(void) { return loopRead < loopLen; }
int NetGetByte(void) { return loopRead < loopLen ? loop[loopRead++] : -1; }
unsigned char NetOpen(void) { return 1; }
void NetClose(void) { }
void NetSuspend(void) { }
void NetResume(void) { }
unsigned char NetStatus(void) { return 0; }

static void emit(const char* name, unsigned char opcode,
                 const unsigned char* payload, unsigned char len)
{
	unsigned int i;

	loopLen = 0;
	loopRead = 0;
	NetSend(opcode, payload, len);

	printf("%s ", name);
	for (i = 0; i < loopLen; ++i) { printf("%02x", loop[i]); }
	printf("\n");
}

/* Round-trips every vector back through the decoder, so encode and decode are
   checked against each other as well as against Go. */
static int roundTrip(unsigned char opcode, const unsigned char* payload,
                     unsigned char len)
{
	loopLen = 0;
	loopRead = 0;
	NetSend(opcode, payload, len);

	if (!NetPoll()) { return 0; }
	if (netFrame.opcode != opcode || netFrame.len != len) { return 0; }
	return len == 0 || memcmp(netFrame.payload, payload, len) == 0;
}

int main(void)
{
	unsigned char payload[NET_MAX_PAYLOAD];
	unsigned int i;
	int failures = 0;

	/* CRC vectors: the check value for CCITT-FALSE over "123456789" is 0x29B1. */
	printf("crc-123456789 %04x\n", NetCRC16((const unsigned char*)"123456789", 9));
	printf("crc-empty %04x\n", NetCRC16((const unsigned char*)"", 0));

	/* NetSend increments a private sequence counter, so vectors are emitted in a
	   fixed order and Go replays the same sequence. */
	emit("empty", 4, 0, 0);

	for (i = 0; i < 11; ++i) { payload[i] = (unsigned char)(i * 17); }
	emit("clframe", 5, payload, 11);

	/* Zero bytes are what COBS exists to handle -- an all-zero payload is the
	   worst case for the encoder. */
	memset(payload, 0, NET_MAX_PAYLOAD);
	emit("zeros", 3, payload, NET_MAX_PAYLOAD);

	for (i = 0; i < NET_MAX_PAYLOAD; ++i) { payload[i] = (unsigned char)(255 - i); }
	emit("maxpayload", 1, payload, NET_MAX_PAYLOAD);

	/* Round trips, including the lengths the protocol actually uses. */
	for (i = 0; i <= NET_MAX_PAYLOAD; ++i) {
		unsigned char j;
		for (j = 0; j < i; ++j) { payload[j] = (unsigned char)(j ^ 0x5a); }
		if (!roundTrip(2, payload, (unsigned char)i)) {
			printf("FAIL round trip at length %u\n", i);
			++failures;
		}
	}

	/* A corrupted CRC must be rejected, not silently accepted. */
	loopLen = 0;
	loopRead = 0;
	NetSend(2, payload, 8);
	loop[4] ^= 0xff;
	if (NetPoll()) {
		printf("FAIL decoder accepted a corrupted frame\n");
		++failures;
	}

	printf(failures ? "FAILURES %d\n" : "OK\n", failures);
	return failures != 0;
}
