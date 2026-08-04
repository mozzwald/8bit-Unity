"""
 * Copyright (c) 2026 8bit-Unity contributors.
 *
 * This software is provided 'as-is', without any express or implied warranty.
 * In no event will the authors be held liable for any damages arising from
 * the use of this software.
 *
 * Permission is granted to anyone to use this software for any purpose,
 * including commercial applications, and to alter it and redistribute it
 * freely, subject to the following restrictions:
 *
 *   1. The origin of this software must not be misrepresented * you must not
 *   claim that you wrote the original software. If you use this software in a
 *   product, an acknowledgment in the product documentation would be
 *   appreciated but is not required.
 *
 *   2. Altered source versions must be plainly marked as such, and must not
 *   be misrepresented as being the original software.
 *
 *   3. This notice may not be removed or altered from any distribution.
 *
 *   4. The names of this software and/or it's copyright holders may not be
 *   used to endorse or promote products derived from this software without
 *   specific prior written permission.
"""

# Print the payload size of a sprite sheet written by AtariSprites.py.
#
# The file is a 16-bit little-endian byte count followed by that many bytes of
# sprite data (colours x frames x height). builder.py captures this value and
# passes it to the compiler as SPRITEDATA, which lets LoadSprites() reserve a
# static buffer instead of calling malloc() -- malloc and free together cost
# ~740 bytes of main RAM on the Atari, for a single allocation that is made once
# at startup and never released.

import io, struct, sys

if len(sys.argv) < 2:
    sys.exit("usage: AtariSpriteSize.py <sprites.dat>")

with io.open(sys.argv[1], 'rb') as f:
    header = f.read(2)
    if len(header) < 2:
        sys.exit("AtariSpriteSize: %s is truncated" % sys.argv[1])
    size = struct.unpack('<H', header)[0]
    actual = len(f.read())

if actual != size:
    sys.exit("AtariSpriteSize: %s declares %d bytes but holds %d"
             % (sys.argv[1], size, actual))

sys.stdout.write("%d\n" % size)
