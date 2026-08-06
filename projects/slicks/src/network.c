
#include "definitions.h"
#include "net_proto.h"

char networkReady = 0;
char chatBuffer[20];
/* udpBuffer[28] removed: it staged 8bit-Hub datagrams. NetStream frames go
   straight through net_proto's ring, and nothing read it. */

unsigned char clIndex, clVersion;
unsigned char clUser[MAX_NAME_LEN+1] = "";
unsigned char clPass[13] = "";

/* Declared unsigned int, not a pointer, to match interface.c:24. The menu code
   walks it with PEEK(packet) / PEEK(++packet), and both are two bytes under
   cc65, so the old mismatch linked by luck.
   See ref/netstream-plan/00-constraints.md section 8. */
unsigned int packet;

/* svFPS/tckNET (the Hub's server tick negotiation) and eData1/eData2 (its event
   payload bytes) are gone: nothing in the game ever read them. */
unsigned char clFrame, svFrame, svMap, svStep;
unsigned char clName[MAX_PLAYERS][MAX_NAME_LEN+1];
clock_t timeRecv, timeSend;

// See slicks.c
extern unsigned char inkColors[];

// See game.c
extern unsigned char gameMap, gameStep;
extern unsigned char gameLineUp[4];
extern unsigned char lapGoal;
extern unsigned int lapBest[MAX_PLAYERS];

// See interface.c
extern unsigned char controlIndex[MAX_PLAYERS];

// See navigation.c
extern Vehicle cars[MAX_PLAYERS];

/*
 * Staging buffer that `packet` points at. It holds either the re-serialised
 * room list in the exact legacy layout MenuServers() parses, or an SV_ERROR
 * message whose text starts at offset 1 (interface.c:808 prints
 * (char*)(packet+1)).
 *
 * Sized for the 12 rooms MenuServers() will display, at 12 name characters
 * each plus a newline: 2 + 12*13.
 */
#define NET_LIST_ROOMS 12
#define NET_LIST_NAME  12
#define NET_BUFFER     (2 + NET_LIST_ROOMS * (NET_LIST_NAME + 1))

static unsigned char netBuffer[NET_BUFFER];

/* SV_INFO is a fixed size: slot, map, step, lapGoal, then per slot an occupancy
   byte and the NUL-padded name. */
#define NET_INFO_LEN (4 + MAX_PLAYERS * (1 + MAX_NAME_LEN + 1))

/* Tick budgets are literals, not multiples of TCK_PER_SEC. On Atari that macro
   is CLOCKS_PER_SEC, which cc65 implements as a runtime call, so every use
   emitted a jsr ___clocks_per_sec plus a 32-bit multiply -- inside the polling
   loops these guard. 60Hz is assumed; on PAL each window is 20% longer, which
   is harmless for what are already deliberately generous timeouts.

   Handshake replies get a generous window: the Atari link is 19200 baud and the
   server may be mid-tick. */
#define NET_REPLY_TICKS 180u		/* 3s @ 60Hz */

/* Silence longer than this means the link is gone. Must outlast a map-change
   suspend, during which the Atari is talking to the disk instead. */
#define NET_TIMEOUT_TICKS 600u		/* 10s @ 60Hz */

/* CL_FRAME is rate limited to the server tick; sending faster only wastes
   bandwidth we do not have at 19200 baud. */
#define NET_FRAME_TICKS 3u		/* 20Hz @ 60Hz */

/* ComLynx is a single open-collector wire carrying both directions, so the
   two ends have to take turns. FujiNet writes the server's bytes onto it and
   then reads back the same count to discard its own echo
   (lib/device/comlynx/netstream.cpp, process_net_packet); anything the Lynx
   transmits during that window is swallowed instead, and the server sees a
   damaged frame. Sending on a free-running timer guarantees the overlap.

   So on Lynx a frame is sent in reply to one arriving, never on a clock --
   the discipline the proven client in ~/build/a8-mcp-demo-game/v6 uses
   ("movement is paced by WORLD_STATE arrivals rather than by any local
   timer", lynx-client/src/main.c:108). The server answers a Lynx only when
   it hears from one, so the wire has a single owner at a time.

   The timer becomes the recovery path: a dropped frame in either direction
   would otherwise stall the exchange for good. */
#define NET_LYNX_REPRIME_TICKS 6u	/* 100ms @ 60Hz */

static unsigned char joined = 0;
static unsigned char pendingEvent = 0;
static unsigned char scratch[NET_MAX_PAYLOAD];

static void PutInt(unsigned char* target, int value)
{
	target[0] = (unsigned char)value;
	target[1] = (unsigned char)(((unsigned int)value) >> 8);
}

static int GetInt(const unsigned char* source)
{
	return (int)(source[0] | (((unsigned int)source[1]) << 8));
}

void NetworkTicket(char ticket)
{
	/* Phase 1 is anonymous: CL_JOIN carries a 32-bit ticket field so the login
	   service can fill it in later without a wire format change. Phase 5 stores
	   the issued ticket here. */
	clVersion = (unsigned char)ticket;
}

void ServerConnect()
{
	networkReady = NetOpen();
	joined = 0;
	pendingEvent = 0;
	timeRecv = clock();
	timeSend = clock();
}

void ServerDisconnect()
{
	if (networkReady) { NetClose(); }
	networkReady = 0;
	joined = 0;
}

/*
 * Rebuilds the legacy list buffer from the per-room SV_LIST frames. The whole
 * list cannot fit one 48-byte payload -- 12 rooms is 193 bytes -- so the server
 * sends one frame per room, each carrying the total. See
 * ref/netstream-plan/01-protocol.md.
 */
void ServerList()
{
	unsigned char total = 0, seen = 0, count = 0;
	unsigned char i, n;
	unsigned char* write;
	clock_t deadline;

	packet = 0;
	if (!networkReady) { return; }

	NetSend(CL_LIST, 0, 0);

	netBuffer[0] = SV_LIST;
	write = &netBuffer[2];
	deadline = clock() + NET_REPLY_TICKS;

	while (clock() < deadline) {
		if (!NetPoll()) { continue; }
		if (netFrame.opcode != SV_LIST || netFrame.len < 3) { continue; }

		total = netFrame.payload[0];
		if (total > NET_LIST_ROOMS) { total = NET_LIST_ROOMS; }

		/* Ignore rooms past what the menu can show rather than overrun. */
		if (netFrame.payload[1] < NET_LIST_ROOMS) {
			n = netFrame.len - 3;
			if (n > NET_LIST_NAME) { n = NET_LIST_NAME; }
			for (i = 0; i < n; ++i) { *write++ = netFrame.payload[2 + i]; }
			*write++ = 10;
			++count;
		}

		if (++seen >= total) { break; }
	}

	if (total == 0) { return; }

	netBuffer[1] = count;
	*write = 0;
	packet = (unsigned int)&netBuffer[0];
	timeRecv = clock();
}

/* Applies an SV_INFO snapshot: our slot, the room's map and step, and the
   name list the scoreboard reads. */
void ServerInfo()
{
	unsigned char i, j;
	const unsigned char* slot;

	clIndex = netFrame.payload[0];
	svMap   = netFrame.payload[1];
	svStep  = netFrame.payload[2];
	lapGoal = netFrame.payload[3];

	for (i = 0; i < MAX_PLAYERS; ++i) {
		slot = &netFrame.payload[4 + i * (1 + MAX_NAME_LEN + 1)];

		if (slot[0] == NET_SLOT_EMPTY) {
			clName[i][0] = 0;
			continue;
		}

		for (j = 0; j < MAX_NAME_LEN; ++j) { clName[i][j] = slot[1 + j]; }
		clName[i][MAX_NAME_LEN] = 0;

		/* The wire carries occupancy, not NET_CONTROL: that is LEN_CONTROL-1
		   and differs per target, so the server cannot name it. Our own slot
		   keeps whatever local control the menu assigned. */
		if (i != clIndex) { controlIndex[i] = NET_CONTROL; }
	}
}

/* Returns the event code from an SV_EVENT frame, or 0. The original returned an
   uninitialised local (constraints section 9). */
unsigned char ServerEvent()
{
	unsigned char event = netFrame.payload[0];
	unsigned char i, n;
	unsigned int  ticks;

	switch (event) {
	case EVENT_RACE:
	case EVENT_MAP:
		svMap = netFrame.payload[2];
		svStep = netFrame.payload[3];
		break;

	case EVENT_LAP:
		/* The server times laps, because game.c:834 only does so in MODE_LOCAL:
		   online clients advance way/lap but never compute a time. data1/data2
		   carry the lap in client ticks, which is the unit PrintBestLap()
		   divides by TCK_PER_SEC (interface.c:474). */
		i = netFrame.payload[1];
		if (i < MAX_PLAYERS) {
			ticks = netFrame.payload[2] | (((unsigned int)netFrame.payload[3]) << 8);
			if (ticks && (!lapBest[i] || ticks < lapBest[i])) { lapBest[i] = ticks; }
		}
		break;

	case EVENT_CHAT:
		if (netFrame.len > 4) {
			n = netFrame.len - 4;
			if (n > sizeof(chatBuffer) - 1) { n = sizeof(chatBuffer) - 1; }
			for (i = 0; i < n; ++i) { chatBuffer[i] = netFrame.payload[4 + i]; }
			chatBuffer[n] = 0;
		}
		break;

	default:
		break;
	}
	return event;
}

/*
 * Applies an SV_FRAME to the remote cars. dx/dy are written as the delta toward
 * the server position -- that is what the LERP consumer at game.c:560-568
 * expects, gated on iCtrl == NET_CONTROL with LERP_THRESHOLD 128.
 */
void ServerFrame()
{
	const unsigned char* read = &netFrame.payload[1];
	unsigned char mask = netFrame.payload[0];
	unsigned char i;
	Vehicle* car;

	for (i = 0; i < MAX_PLAYERS; ++i) {
		if (!(mask & (1 << i))) { continue; }

		car = &cars[i];
		if (i != clIndex && controlIndex[i] == NET_CONTROL) {
			car->dx = GetInt(&read[0]) - car->x;
			car->dy = GetInt(&read[2]) - car->y;
			car->ang1 = GetInt(&read[4]);
			car->vel = GetInt(&read[6]);
			car->way = read[8];
			car->lap = (signed char)read[9];
		}
		read += 10;
	}
	++svFrame;
}

void ServerAuth()
{
	/* Phase 5 fills this in: SV_AUTH carries the login service's verdict. Until
	   then the handshake is anonymous and the server never sends it. */
}

unsigned char ClientJoin(char game)
{
	unsigned char len = 0;
	unsigned char i;
	clock_t deadline;

	packet = 0;
	if (!networkReady) { return ERR_TIMEOUT; }

	/* ticket u32 -- zero until Phase 5 issues one */
	scratch[len++] = 0;
	scratch[len++] = 0;
	scratch[len++] = 0;
	scratch[len++] = 0;
	scratch[len++] = NET_PLATFORM;
	scratch[len++] = NET_VERSION;
	scratch[len++] = (unsigned char)game;
	for (i = 0; i <= MAX_NAME_LEN; ++i) { scratch[len++] = clUser[i]; }

	NetSend(CL_JOIN, scratch, len);

	/* ns_init_netstream() does not check the SIOV status, so a missing FujiNet
	   or an unreachable host still looks like success. This timeout is the real
	   detector. See constraints section 3. */
	deadline = clock() + NET_REPLY_TICKS;
	while (clock() < deadline) {
		if (!NetPoll()) { continue; }

		if (netFrame.opcode == SV_INFO) {
			if (netFrame.len < NET_INFO_LEN) { return ERR_CORRUPT; }
			ServerInfo();
			joined = 1;
			timeRecv = clock();
			return 1;
		}

		if (netFrame.opcode == SV_ERROR) {
			for (i = 0; i < netFrame.len && i < NET_BUFFER - 2; ++i) {
				netBuffer[1 + i] = netFrame.payload[i];
			}
			netBuffer[0] = SV_ERROR;
			netBuffer[1 + i] = 0;
			packet = (unsigned int)&netBuffer[0];
			return ERR_MESSAGE;
		}
	}
	return ERR_TIMEOUT;
}

void ClientFrame()
{
	Vehicle* car = &cars[clIndex];
	unsigned char len = 0;

	if (!joined) { return; }

	scratch[len++] = car->joy;
	PutInt(&scratch[len], car->x);    len += 2;
	PutInt(&scratch[len], car->y);    len += 2;
	PutInt(&scratch[len], car->ang1); len += 2;
	PutInt(&scratch[len], car->vel);  len += 2;
	scratch[len++] = car->way;
	scratch[len++] = (unsigned char)car->lap;

	NetSend(CL_FRAME, scratch, len);
	timeSend = clock();
	++clFrame;
}

unsigned char ClientReady()
{
	clock_t deadline;

	if (!networkReady) { return ERR_TIMEOUT; }

	NetSend(CL_READY, 0, 0);

	deadline = clock() + NET_REPLY_TICKS;
	while (clock() < deadline) {
		if (!NetPoll()) { continue; }
		timeRecv = clock();

		if (netFrame.opcode == SV_OK) { return 1; }

		/* The room moved on while we were loading -- follow it. */
		if (netFrame.opcode == SV_EVENT && ServerEvent() == EVENT_MAP) {
			return EVENT_MAP;
		}
	}
	return ERR_TIMEOUT;
}

void ClientEvent(char event)
{
	unsigned char len = 0;
	unsigned char i;

	if (!networkReady) { return; }

	scratch[len++] = (unsigned char)event;
	scratch[len++] = clIndex;
	scratch[len++] = 0;
	scratch[len++] = 0;

	if (event == EVENT_CHAT) {
		for (i = 0; chatBuffer[i] && len < NET_MAX_PAYLOAD - 1; ++i) {
			scratch[len++] = chatBuffer[i];
		}
		scratch[len++] = 0;
	}

	NetSend(CL_EVENT, scratch, len);
}

void ClientLeave()
{
	if (!networkReady) { return; }
	NetSend(CL_LEAVE, 0, 0);
	joined = 0;
}

/*
 * The per-frame pump, called from game.c:981 / game.c:984 and the Lynx pause
 * loop at consoles.c:349. Sends our car at the server tick rate, drains RX, and
 * reports the one event the caller acts on this frame.
 */
unsigned char NetworkUpdate()
{
	unsigned char event = 0;
	unsigned char heard = 0;

	if (!networkReady) { return 0; }

#if !defined(__LYNX__)
	if (clock() - timeSend >= NET_FRAME_TICKS) { ClientFrame(); }
#endif

	while (NetPoll()) {
		timeRecv = clock();
		heard = 1;

		switch (netFrame.opcode) {
		case SV_FRAME:
			ServerFrame();
			break;

		case SV_INFO:
			if (netFrame.len >= NET_INFO_LEN) { ServerInfo(); }
			break;

		case SV_EVENT:
			/* EVENT_RACE and EVENT_MAP both unwind the caller's loop, so only
			   one can be reported per frame. Hold the other for the next call
			   rather than dropping it. */
			if (event) { pendingEvent = ServerEvent(); }
			else { event = ServerEvent(); }
			break;

		default:
			break;
		}
	}

#if defined(__LYNX__)
	/* Take our turn on the wire: the bus is idle now that the reply is
	   drained. Falls back to the timer if nothing arrived, so a lost frame
	   re-primes the exchange instead of deadlocking it. */
	if (heard || clock() - timeSend >= NET_LYNX_REPRIME_TICKS) { ClientFrame(); }
#else
	(void)heard;
#endif

	if (!event && pendingEvent) {
		event = pendingEvent;
		pendingEvent = 0;
	}

	if (event == EVENT_RACE || event == EVENT_MAP) { return event; }

	if (clock() - timeRecv > NET_TIMEOUT_TICKS) { return ERR_TIMEOUT; }
	return 0;
}
