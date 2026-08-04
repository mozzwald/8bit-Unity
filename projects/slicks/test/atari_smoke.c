#include <conio.h>
#include <stdlib.h>

#include "ns_api.h"

#ifndef SLICKS_SERVER_HOST
#define SLICKS_SERVER_HOST "192.168.1.120"
#endif

#ifndef SLICKS_SERVER_PORT
#define SLICKS_SERVER_PORT 8320
#endif

#define NETSTREAM_FLAGS 0x06
#define NETSTREAM_BAUD 19200

static unsigned int swap16(unsigned int value)
{
    return (unsigned int)((value << 8) | (value >> 8));
}

int main(void)
{
    unsigned long tx = 0;
    unsigned long rx = 0;

    clrscr();
    cprintf("Slicks NetStream smoke\r\n");
    cprintf("%s:%u UDP @ %u\r\n", SLICKS_SERVER_HOST,
            (unsigned)SLICKS_SERVER_PORT, NETSTREAM_BAUD);

    if (ns_init_netstream(SLICKS_SERVER_HOST, NETSTREAM_FLAGS, NETSTREAM_BAUD,
                          swap16(SLICKS_SERVER_PORT)) != 0) {
        cprintf("NetStream init failed\r\n");
        return EXIT_FAILURE;
    }
    ns_begin_stream();
    cprintf("Connected. Type; ESC exits.\r\n");

    while (1) {
        int value;
        unsigned char status = ns_get_status();

        while (ns_bytes_avail() != 0) {
            value = ns_recv_byte();
            if (value >= 0) {
                cputc((char)value);
                ++rx;
            }
        }

        gotoxy(0, 22);
        cprintf("TX:%5lu RX:%5lu STATUS:%02X ", tx, rx, status);

        if (kbhit()) {
            value = cgetc();
            if (value == 0x1b) {
                break;
            }
            while (ns_send_byte((unsigned char)value) != 0) {
            }
            ++tx;
        }
    }

    ns_end_stream();
    cprintf("\r\nStream closed.\r\n");
    return EXIT_SUCCESS;
}
