#!/usr/bin/env bash
# Build and check the FujiNet NetStream clients for Atari 8-bit and Lynx.
#
#   ./projects/slicks/build_netstream.sh              # gate + codec test + smoke clients
#   ./projects/slicks/build_netstream.sh gate         # POKEY channel 3/4 gate only
#   ./projects/slicks/build_netstream.sh test         # codec conformance vectors only
#   SLICKS_SERVER_HOST=slicks.fujinet.online ./projects/slicks/build_netstream.sh
#
# Outputs land in build/.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

# cl65 otherwise falls through to /usr/include and picks up the host libc.
export CC65_HOME="${CC65_HOME:-/usr/share/cc65}"

SLICKS_SERVER_HOST="${SLICKS_SERVER_HOST:-192.168.1.120}"
SLICKS_SERVER_PORT="${SLICKS_SERVER_PORT:-8320}"
# Pinned at 19200 so a MOTOR-only suspend is baud-transparent. Not a tuning knob;
# see ref/netstream-plan/00-constraints.md section 1, trap 2.
SLICKS_BAUD="${SLICKS_BAUD:-19200}"
SLICKS_RX_RING="${SLICKS_RX_RING:-256}"
OUT="$ROOT/build"

DEFINES=(
	-D "SLICKS_SERVER_HOST=\"$SLICKS_SERVER_HOST\""
	-D "SLICKS_SERVER_PORT=$SLICKS_SERVER_PORT"
)
GAME_DEFINES=(
	"${DEFINES[@]}"
	-D __NETSTREAM__
	-D "SLICKS_BAUD=$SLICKS_BAUD"
	-D CHUNKSIZE=0x1000 -D SPRITEFRAMES=32 -D SPRITEWIDTH=8 -D SPRITEHEIGHT=16
)

mkdir -p "$OUT"

# POKEY channels 3 and 4 are the NetStream bit clock on Atari: the handler runs
# AUDCTL=$28 with them joined. Any other write to AUDF3/AUDC3, AUDF4/AUDC4,
# AUDCTL or SKCTL corrupts or kills the link.
# See ref/netstream-plan/00-constraints.md section 2.
#
# This inspects the GENERATED ASSEMBLY rather than the source. Grepping the
# source reports every guarded site as a hit; what actually matters is whether a
# write survives into a __NETSTREAM__ build.
check_pokey() {
	echo "==> POKEY channel 3/4 gate"
	local work="$OUT/gate"
	rm -rf "$work"
	mkdir -p "$work"

	local file out
	for file in unity/sound/sfx.c unity/sound/music.c projects/slicks/src/sfx.c; do
		out="$work/$(echo "$file" | tr / _).s"
		cc65 -t atari -O "${GAME_DEFINES[@]}" -I unity -I projects/slicks/src \
			-o "$out" "$file"
	done

	local hits
	hits="$(grep -nE '\$D20[4-8]|\$D20F' "$work"/*.s || true)"
	if [ -n "$hits" ]; then
		echo "ERROR: a __NETSTREAM__ build still writes POKEY channel 3/4 registers:" >&2
		echo "$hits" >&2
		return 1
	fi

	# The SFX VBI is hand-written assembly, so check its loop bound directly.
	if ! grep -q 'cpy #2' unity/targets/atari/POKEY.s; then
		echo "ERROR: unity/targets/atari/POKEY.s has no NETSTREAM loop bound" >&2
		return 1
	fi
	echo "    clean"
}

# The client codec has to agree with server/internal/proto byte for byte.
# This compiles net_proto.c natively and emits the vectors that
# TestClientGoldenVectors checks.
check_codec() {
	echo "==> codec conformance"
	gcc -O2 -I projects/slicks/test/host_stubs -I projects/slicks/src \
		-o "$OUT/host_proto_test" projects/slicks/test/host_proto_test.c
	"$OUT/host_proto_test"
	if command -v go >/dev/null; then
		(cd server && go test ./internal/proto/ -run 'Golden|CRCCheck')
	fi
}

build_atari_smoke() {
	echo "==> atari smoke client"
	cl65 -t atari -O -o "$OUT/smoke.xex" "${DEFINES[@]}" \
		--asm-define "INPUT_BUFSIZE=$SLICKS_RX_RING" \
		-I projects/slicks/src \
		projects/slicks/test/atari_smoke.c \
		unity/targets/atari/netstream.s

	rm -rf "$OUT/atr"
	mkdir -p "$OUT/atr"
	cp "$OUT/smoke.xex" "$OUT/atr/AUTORUN.XEX"
	dir2atr -b Dos20 "$OUT/smoke.atr" "$OUT/atr" >/dev/null
	echo "    $OUT/smoke.atr"
}

# cc65 ships the Lynx serial and TGI drivers as loadable .ser/.tgi modules. co65
# converts them for static linking -- note it emits .s, not an object file,
# despite what the -o name suggests.
build_lynx_smoke() {
	echo "==> lynx smoke client"
	co65 --code-label _lynx_comlynx_ser \
		-o "$OUT/lynx-comlynx.s" "$CC65_HOME/target/lynx/drv/ser/lynx-comlynx.ser"
	co65 --code-label _lynx_160_102_16_tgi \
		-o "$OUT/lynx-tgi.s" "$CC65_HOME/target/lynx/drv/tgi/lynx-160-102-16.tgi"

	cl65 -t lynx -O -o "$OUT/smoke.lnx" "${DEFINES[@]}" \
		projects/slicks/test/lynx_smoke.c \
		"$OUT/lynx-comlynx.s" "$OUT/lynx-tgi.s"
	echo "    $OUT/smoke.lnx"
}

# Syntax-checks every game source against both targets. This is not a link: the
# playable .atr and .lnx need converted assets, which builder.py produces with
# Python 3 and pygubu. See the Phase 2 status notes.
check_game_sources() {
	echo "==> game sources"
	local work="$OUT/check"
	rm -rf "$work"
	mkdir -p "$work"

	# The Lynx build carves fixed regions out of cart RAM; builder.py normally
	# supplies these from the project's memory settings.
	local lynx_defines=(-D MUSICSIZE=0x1000 -D SHAREDSIZE=0x0800 -D __KEYBOARD__)

	local file failed=0
	for file in projects/slicks/src/*.c; do
		if ! cc65 -t atari -O "${GAME_DEFINES[@]}" -I unity -I projects/slicks/src \
			-o "$work/$(basename "$file" .c)-atari.s" "$file" 2>"$work/err"; then
			grep -v 'Warning:' "$work/err" >&2 || true
			failed=1
		fi
		if ! cc65 -t lynx --cpu 65SC02 -O "${GAME_DEFINES[@]}" "${lynx_defines[@]}" \
			-I unity -I projects/slicks/src \
			-o "$work/$(basename "$file" .c)-lynx.s" "$file" 2>"$work/err"; then
			grep -v 'Warning:' "$work/err" >&2 || true
			failed=1
		fi
	done
	if [ "$failed" != 0 ]; then
		echo "ERROR: game sources do not compile" >&2
		return 1
	fi
	ca65 -t atari -D NETSTREAM -D "INPUT_BUFSIZE=$SLICKS_RX_RING" \
		-o "$work/netstream.o" unity/targets/atari/netstream.s
	ca65 -t atari -D NETSTREAM -o "$work/pokey.o" unity/targets/atari/POKEY.s
	echo "    all sources compile for both targets"
}

echo "server $SLICKS_SERVER_HOST:$SLICKS_SERVER_PORT, baud $SLICKS_BAUD"

case "${1:-all}" in
	gate) check_pokey ;;
	test) check_codec ;;
	all)
		check_pokey
		check_codec
		check_game_sources
		build_atari_smoke
		build_lynx_smoke
		echo "done"
		;;
	*)
		echo "usage: $0 [all|gate|test]" >&2
		exit 2
		;;
esac
