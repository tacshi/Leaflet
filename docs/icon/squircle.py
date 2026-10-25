# Apple's icon tile: a continuous-curvature squircle (superellipse) on the
# macOS grid, 824 wide at 100,100 on a 1024 canvas.
import math
cx, cy, r, n = 512, 512, 412, 4.6
pts = []
for i in range(720):
    t = 2 * math.pi * i / 720
    c, s = math.cos(t), math.sin(t)
    x = cx + r * math.copysign(abs(c) ** (2 / n), c)
    y = cy + r * math.copysign(abs(s) ** (2 / n), s)
    pts.append(f"{x:.1f} {y:.1f}")
print("M" + " L".join(pts) + " Z")
