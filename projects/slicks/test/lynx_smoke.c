#include <lynx.h>
#include <joystick.h>
#include <serial.h>
#include <stdio.h>
#include <tgi.h>

#ifndef SLICKS_SERVER_HOST
#define SLICKS_SERVER_HOST "192.168.1.120"
#endif

#ifndef SLICKS_SERVER_PORT
#define SLICKS_SERVER_PORT 8320
#endif

#define FUJI_DEVICE_ID 0x70
#define FUJICMD_ENABLE_NETSTREAM 0xF0
#define FUJICMD_ACK 0x06
#define FUJICMD_NAK 0x15
#define NETSTREAM_FLAGS 0x03 /* TCP | REGISTER */

static unsigned char xor8(const unsigned char* buffer, unsigned char length)
{
    unsigned char value = 0;
    while (length != 0) {
        value ^= *buffer++;
        --length;
    }
    return value;
}

static unsigned char get_byte_timeout(unsigned char* value)
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

static unsigned char wait_for_ack(void)
{
    unsigned char value;
    while (get_byte_timeout(&value)) {
        if (value == FUJICMD_ACK) {
            return 1;
        }
        if (value == FUJICMD_NAK) {
            return 0;
        }
    }
    return 0;
}

static unsigned char enable_netstream(void)
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

    while (ser_put(FUJI_DEVICE_ID) != SER_ERR_OK) {}
    while (ser_put(0) != SER_ERR_OK) {}
    while (ser_put((char)length) != SER_ERR_OK) {}
    for (i = 0; i < length; ++i) {
        while (ser_put((char)payload[i]) != SER_ERR_OK) {}
    }
    while (ser_put((char)xor8(payload, length)) != SER_ERR_OK) {}
    /* FujiNet switches ComLynx to raw NetStream immediately after the
       command ACK. There is no second command-completion ACK to consume. */
    return wait_for_ack();
}

static void draw_status(unsigned long tx, unsigned long rx, unsigned char error)
{
    char line[32];
    tgi_clear();
    tgi_outtextxy(4, 4, "Slicks NetStream smoke");
    tgi_outtextxy(4, 16, SLICKS_SERVER_HOST);
    sprintf(line, "TCP %u  TX %lu  RX %lu", (unsigned)SLICKS_SERVER_PORT, tx, rx);
    tgi_outtextxy(4, 28, line);
    sprintf(line, "serial status %02X", error);
    tgi_outtextxy(4, 40, line);
    tgi_outtextxy(4, 60, "A/B sends a byte");
}

void main(void)
{
    const struct ser_params params = {
        SER_BAUD_62500, SER_BITS_8, SER_STOP_1, SER_PAR_ODD, SER_HS_NONE
    };
    unsigned long tx = 0;
    unsigned long rx = 0;
    unsigned char status = 0;
    unsigned char redraw = 1;
    char byte;

    tgi_install(lynx_160_102_16_tgi);
    tgi_init();
    while (tgi_busy()) {}
    tgi_setframerate(60);
    tgi_setcolor(TGI_COLOR_WHITE);
    joy_install(lynx_stdjoy_joy);

    if (ser_install(lynx_comlynx_ser) != SER_ERR_OK ||
        ser_open(&params) != SER_ERR_OK || !enable_netstream()) {
        tgi_clear();
        tgi_outtextxy(4, 4, "NetStream setup failed");
        while (1) {}
    }

    while (1) {
        while (ser_get(&byte) == SER_ERR_OK) {
            ++rx;
            redraw = 1;
        }
        if (joy_read(0) & (JOY_BTN_1_MASK | JOY_BTN_2_MASK)) {
            if (ser_put('L') == SER_ERR_OK) {
                ++tx;
                redraw = 1;
            }
        }
        ser_status(&status);
        if (redraw && !tgi_busy()) {
            draw_status(tx, rx, status);
            tgi_updatedisplay();
            redraw = 0;
        }
    }
}
