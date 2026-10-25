# A leaf for Leaflet's icon, drawn from curves: an asymmetric outline, a
# curving midrib, and veins that arc toward the tip and stop short of the
# edge. Writes the leaf's parts as SVG fragments in leaf coordinates (tip
# up, base at 0,300).
import math, json

def cubic(p0, p1, p2, p3, t):
    return tuple((1-t)**3*p0[i] + 3*(1-t)**2*t*p1[i] + 3*(1-t)*t*t*p2[i] + t**3*p3[i] for i in range(2))

# Outline: base, left shoulder, tip, right shoulder, back to base.
segs = [
    ((0, 300), (-118, 262), (-232, 140), (-228, -20)),
    ((-228, -20), (-222, -170), (-120, -282), (36, -334)),
    ((36, -334), (150, -262), (214, -120), (204, 12)),
    ((204, 12), (192, 150), (104, 262), (0, 300)),
]
outline = "M0 300 " + " ".join(f"C{a[0]} {a[1]} {b[0]} {b[1]} {c[0]} {c[1]}" for _, a, b, c in segs) + " Z"
poly = [cubic(*s, i / 200) for s in segs for i in range(200)]

def inside(p):
    x, y = p; n = len(poly); c = False
    for i in range(n):
        x1, y1 = poly[i]; x2, y2 = poly[(i + 1) % n]
        if (y1 > y) != (y2 > y) and x < (x2 - x1) * (y - y1) / (y2 - y1) + x1:
            c = not c
    return c

def edge_dist(p):
    return min(math.hypot(p[0] - q[0], p[1] - q[1]) for q in poly[::2])

# Midrib: from the base to just short of the tip, a gentle curve.
mid = ((0, 300), (-22, 150), (-12, -120), (30, -318))
midrib = f"M0 300 C-22 150 -12 -120 30 -318"

veins = []
for side in (-1, 1):
    for k, t in enumerate([0.16, 0.30, 0.44, 0.58, 0.72]):
        t += 0.06 if side > 0 else 0          # alternate, as leaves do
        p = cubic(*mid, t)
        q = cubic(*mid, t + 0.01)
        tx, ty = q[0] - p[0], q[1] - p[1]; l = math.hypot(tx, ty); tx, ty = tx / l, ty / l
        # the normal on this side: left of the midrib for -1, right for +1
        nx, ny = (-ty, tx) if side > 0 else (ty, -tx)
        if (nx > 0) != (side > 0):
            nx, ny = -nx, -ny
        # leave the midrib at 50 degrees, heading toward the tip
        a = math.radians(50)
        dx, dy = nx * math.sin(a) + tx * math.cos(a), ny * math.sin(a) + ty * math.cos(a)
        L = 0
        while edge_dist((p[0] + dx * L, p[1] + dy * L)) > 34 and inside((p[0] + dx * L, p[1] + dy * L)):
            L += 1
        L *= 0.98
        end = (p[0] + dx * L + tx * L * 0.25, p[1] + dy * L + ty * L * 0.25)
        c1 = (p[0] + dx * L * 0.45, p[1] + dy * L * 0.45)
        veins.append(f"M{p[0]:.1f} {p[1]:.1f} Q{c1[0]:.1f} {c1[1]:.1f} {end[0]:.1f} {end[1]:.1f}")

json.dump({"outline": outline, "midrib": midrib, "veins": veins,
           "right": "M36 -334 C150 -262 214 -120 204 12 C192 150 104 262 0 300 C-22 150 -12 -120 30 -318 Z"},
          open("leaf.json", "w"))
print(len(veins), "veins")
