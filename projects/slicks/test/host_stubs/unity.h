/* Host stub for the 8bit-Unity SDK header, so definitions.h parses natively and
   net_proto.c can be conformance-tested against the Go codec. Only the handful
   of things definitions.h and net_proto.c actually reference. */
#ifndef SLICKS_HOST_UNITY_H
#define SLICKS_HOST_UNITY_H

#include <stdlib.h>
#include <string.h>
#include <time.h>

#define TCK_PER_SEC CLOCKS_PER_SEC

#define PEEK(addr)       (*(unsigned char*)(addr))
#define POKE(addr, val)  (*(unsigned char*)(addr) = (val))
#define POKEW(addr, val) (*(unsigned int*)(addr) = (val))

#define __fastcall__

#endif
