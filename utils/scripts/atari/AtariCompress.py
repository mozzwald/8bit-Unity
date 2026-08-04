"""
 * Copyright (c) 2022 Anthony Beaucamp.
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

import io, os, struct, subprocess, sys

# Retrieve command params
xexFile = sys.argv[1]
rawFile = xexFile[0:-4] + ".raw"
sfxFile = rawFile+".zx0"

# Sub-processes
#
# The decruncher in xboot.obx / unity/targets/atari/decrunch.s reads the ZX0 v2
# bitstream. ZX0 v1 emits a different, silently incompatible stream: the Atari
# decompresses garbage, runs past the end of the block and walks its destination
# pointer through the hardware registers at $D400. So the version is checked
# here rather than left to fail on the target.
def zx0_version(cmd):
    try:
        out = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        banner = out.stdout.decode("latin-1", "replace")
    except OSError:
        return None
    for line in banner.splitlines():
        if "ZX0 v" in line:
            return line.split("ZX0 v", 1)[1].split(":", 1)[0].strip()
    return None

def pick_zx0():
    candidates = []
    override = os.environ.get("ZX0")
    if override:
        candidates.append(override.split())
    if "nt" == os.name:
        candidates.append([os.path.join("utils", "scripts", "zx0.exe")])
    else:
        candidates.append([os.path.join("utils", "zx0")])
        candidates.append(["zx0"])
    tried = []
    for cmd in candidates:
        version = zx0_version(cmd)
        if version is None:
            tried.append("%s (not found)" % " ".join(cmd))
        elif version.startswith("1."):
            tried.append("%s (v%s, needs v2)" % (" ".join(cmd), version))
        else:
            return cmd
    sys.exit("AtariCompress: no ZX0 v2 compressor found. Tried:\n  " +
             "\n  ".join(tried))

zx0 = pick_zx0()

# Read input file
fin = io.open(xexFile, 'rb')
data = fin.read()
fin.close()

# Create output file and include decompressor
fout = io.open(xexFile, 'wb')

# 2 bytes atari header (i.e $ff $ff)
fout.write(bytes([0xFF, 0xFF]))

# Write blocks as raw files and compress them
i = 2
blocks = []
while (i<len(data)):
    try:
        # Read header
        start = struct.unpack('H', data[i+0:i+2])[0]
        end   = struct.unpack('H', data[i+2:i+4])[0]
        size  = end-start+1
        blocks.append([start,end])
        
        # Write raw data
        f = io.open(rawFile, 'wb')
        f.write(data[i+4:i+4+size])
        f.close()
        
        # Compress raw data
        subprocess.check_call(zx0 + ["-f", rawFile])
        f = io.open(sfxFile, 'rb')
        sfx = f.read()
        f.close()        
        
        # Append to compressed file
        fout.write(data[i+0:i+2])   # Load address
        fout.write(bytes([0x00, 0x00, 0x02])) # Compressor header
        fout.write(sfx)
        fout.write(data[i+4+size:i+4+size+6])   # Run code
        
        #Clean-up
        os.remove(rawFile)
        os.remove(sfxFile)
        
        # Move to next block
        i = i+4+size+6
    except:
        break
        
fout.close()
