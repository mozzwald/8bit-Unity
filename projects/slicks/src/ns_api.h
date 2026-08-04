#ifndef SLICKS_NS_API_H
#define SLICKS_NS_API_H

/* FujiNet NetStream CA65 linked-handler API. */
void __fastcall__ ns_begin_stream(void);
void __fastcall__ ns_end_stream(void);
unsigned char __fastcall__ ns_get_version(void);
unsigned int __fastcall__ ns_get_base(void);
unsigned char __fastcall__ ns_send_byte(unsigned char b);
int __fastcall__ ns_recv_byte(void);
unsigned int __fastcall__ ns_bytes_avail(void);
unsigned char __fastcall__ ns_get_status(void);
unsigned char __fastcall__ ns_get_video_std(void);
unsigned char __fastcall__ ns_init_netstream(const char* host,
                                             unsigned char flags,
                                             unsigned int nominal_baud,
                                             unsigned int port_swapped);
unsigned char __fastcall__ ns_get_final_flags(void);
unsigned char __fastcall__ ns_get_final_audf3(void);
unsigned char __fastcall__ ns_get_final_audf4(void);

/* MOTOR-only suspend, added to the vendored handler. Keeps the socket and
   netstreamActive alive across disk I/O; see net_atari.c. */
void __fastcall__ ns_suspend(void);
void __fastcall__ ns_resume(void);

#endif
