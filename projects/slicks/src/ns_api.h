#ifndef SLICKS_NS_API_H
#define SLICKS_NS_API_H

/* FujiNet NetStream CA65 linked-handler API.
 *
 * The handler's diagnostic getters (ns_get_version, ns_get_base,
 * ns_get_video_std, ns_get_final_flags, ns_get_final_audf3/4) had no caller and
 * were dropped along with the Altirra jump table they dispatched through. If a
 * debug HUD ever needs them, restore them in unity/targets/atari/netstream.s. */
void __fastcall__ ns_begin_stream(void);
void __fastcall__ ns_end_stream(void);
unsigned char __fastcall__ ns_send_byte(unsigned char b);
int __fastcall__ ns_recv_byte(void);
unsigned int __fastcall__ ns_bytes_avail(void);
unsigned char __fastcall__ ns_get_status(void);
unsigned char __fastcall__ ns_init_netstream(const char* host,
                                             unsigned char flags,
                                             unsigned int nominal_baud,
                                             unsigned int port_swapped);

/* MOTOR-only suspend, added to the vendored handler. Keeps the socket and
   netstreamActive alive across disk I/O; see net_atari.c. */
void __fastcall__ ns_suspend(void);
void __fastcall__ ns_resume(void);

#endif
