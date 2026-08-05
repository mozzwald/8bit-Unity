#!/usr/bin/env bash
# Build a playable Atari NetStream disk without going through builder.py's GUI.
#
#   ./projects/slicks/build_atari_disk.sh                       # 127.0.0.1:8320
#   SLICKS_SERVER_HOST=192.168.1.120 ./projects/slicks/build_atari_disk.sh
#   ./projects/slicks/build_atari_disk.sh build/my-disk.atr
#
# This mirrors exactly what utils/scripts/builder.py emits for SLICKS_NETSTREAM=1
# on the atari64k target: assets are converted first so the sprite sheet's size
# is known, then the library, the game, the loader and the boot chain.
#
# Requires cl65/ca65/ar65 (cc65), dir2atr, and python3. exomizer and zx0 come
# from utils/ unless $EXOMIZER / $ZX0 override them.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

export CC65_HOME="${CC65_HOME:-/usr/share/cc65}"
HOST="${SLICKS_SERVER_HOST:-127.0.0.1}"
PORT="${SLICKS_SERVER_PORT:-8320}"
BAUD="${SLICKS_BAUD:-31250}"
RXRING="${SLICKS_RX_RING:-256}"
OUT="${1:-build/slicks-demo-atari64k-${HOST//./_}.atr}"

LIB="build/[libs]/unity-atari64k-netstrm.lib"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
DISK="$WORK/atari"
mkdir -p "$DISK" "build/[libs]" "build/[maps]"

echo "==> assets"
for png in projects/slicks/bitmaps/*-atari.png; do
	name="$(basename "$png" -atari.png)"
	python3 utils/scripts/atari/AtariBitmap.py double crunch "$png" "$DISK/$name.img" >/dev/null
done
python3 utils/scripts/atari/AtariSprites.py \
	projects/slicks/sprites/sprites-atari.png "$DISK/sprites.dat" 10 >/dev/null
cp projects/slicks/navigation/*.nav "$DISK/"

# The sheet's byte count is colours x frames x height and only exists once the
# PNG has been converted, so it is read back here and handed to the compiler.
# LoadSprites() uses it to size a static buffer instead of pulling in malloc.
SPRITEDATA="$(python3 utils/scripts/atari/AtariSpriteSize.py "$DISK/sprites.dat")"
echo "    SPRITEDATA=$SPRITEDATA"

DEFS=(-D CHUNKSIZE=0x0001 -D SPRITEFRAMES=18 -D SPRITEWIDTH=8 -D SPRITEHEIGHT=10
      -D "SPRITEDATA=$SPRITEDATA" -D __NETSTREAM__ -D "SLICKS_BAUD=$BAUD"
      -D NETSTREAM -D "INPUT_BUFSIZE=$RXRING" -D __DECRUNCH__)

echo "==> unity library"
# Always rebuilt. It is compiled with SPRITEDATA and the __NETSTREAM__ defines,
# so a copy left over from a different configuration links cleanly and fails on
# hardware -- silently, and a long way from the cause.
if [ ! -f build/slicks-demo-atari64k.sh ]; then
	echo "ERROR: build/slicks-demo-atari64k.sh not found -- run builder.py once" >&2
	exit 1
fi
sed -n '12,178p' build/slicks-demo-atari64k.sh \
	| sed "s/-D SPRITEHEIGHT=10/-D SPRITEHEIGHT=10 -D SPRITEDATA=$SPRITEDATA/" \
	> "$WORK/lib.sh"
bash "$WORK/lib.sh" > "$WORK/lib.log" 2>&1 || { tail -20 "$WORK/lib.log" >&2; exit 1; }

# builder.py deletes these at the end of its run; a partial run leaves them in
# the source tree, where they are neither wanted nor tracked.
sed -n '180p' build/slicks-demo-atari64k.sh | tr ' ' '\n' | grep -E '\.(s|o)$' \
	| while read -r stale; do [ -f "$stale" ] && rm -f "$stale"; done || true

echo "==> game  (server $HOST:$PORT)"
cl65 -o "$WORK/netstrm.xex" -m "build/[maps]/slicks-demo-atari64k-netstrm.map" \
	"${DEFS[@]}" -Cl -O -t atarixl -C unity/targets/atari/atarixl-netstream.cfg \
	-Wl -D,__STACKSIZE__=\$0400 -Wl -D,CHUNKSIZE=\$0001 -I unity \
	-Wc "-DSLICKS_SERVER_HOST=\"$HOST\"" -Wc "-DSLICKS_SERVER_PORT=$PORT" \
	--asm-define NETSTREAM --asm-define "INPUT_BUFSIZE=$RXRING" \
	projects/slicks/src/*.c \
	unity/targets/atari/POKEY.s unity/targets/atari/netstream.s \
	unity/targets/atari/netstream-chunk.s "$LIB" 2>&1 | grep -v Warning || true
[ -f "$WORK/netstrm.xex" ] || { echo "ERROR: link failed" >&2; exit 1; }

cp "$WORK/netstrm.xex" "$DISK/netstrm.xex"
python3 utils/scripts/atari/AtariCompress.py "$DISK/netstrm.xex" >/dev/null

echo "==> boot chain"
# The loader launches netstrm.xex through xBIOS. It cannot be merged into
# XAUTORUN: an atarixl program's shadow-RAM prep runs mid-load and banks out the
# OS ROM the boot loader reads sectors through.
cl65 -o "$WORK/loader.bin" -Cl -O -t atarixl -C atarixl-largehimem.cfg \
	-D __NETSTREAM__ -I unity unity/targets/atari/loader.c "$LIB" 2>/dev/null
python3 utils/scripts/atari/AtariCompress.py "$WORK/loader.bin" >/dev/null
cl65 -o "$WORK/basicoff.bin" -t atari -C atari-asm.cfg unity/targets/atari/BASICOFF.s
python3 utils/scripts/atari/AtariMerge.py "$DISK/xautorun" \
	"$WORK/basicoff.bin" "$WORK/loader.bin" >/dev/null
cp utils/scripts/atari/xbios4.obx "$DISK/autorun"

dir2atr -mD -B utils/scripts/atari/xboot.obx "$OUT" "$DISK" >/dev/null
echo "==> $OUT"
