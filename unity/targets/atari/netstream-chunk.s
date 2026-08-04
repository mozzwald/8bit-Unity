; Link-time bound check for Atari NetStream builds.
;
; The main chunk must stop short of BITMAPRAM2-8: LoadBitmap() reads the
; compressed frame-2 stream to $7008 and decrunches it over $7010-$8F4F. Without
; this the linker silently places code and data across that buffer and the game
; hangs on its first bitmap load, having overwritten itself.
;
; Pulled in by the __NETCHUNK__ import in atarixl-netstream.cfg.

	.export		__NETCHUNK__: absolute = 1

	.import		__MAIN_LAST__

	.assert		__MAIN_LAST__ <= $7008, error, "NetStream image overruns bitmap frame 2 at $7010 -- see unity/targets/atari/atarixl-netstream.cfg"
