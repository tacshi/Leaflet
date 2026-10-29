# Composes Leaflet's app icon: a sheet of lined paper on Apple's squircle
# tile, on the macOS grid, with a leaf lying on it. To change it, edit
# these scripts in this directory, then:
#
#   python3 squircle.py > squircle.d && python3 leaf.py && python3 compose.py
#   rsvg-convert -w 1024 -h 1024 icon.svg -o ../../resources/icon.png
#
# A public page's favicon is the tile alone, without the grid's margin:
#
#   sed 's|viewBox="0 0 1024 1024"|viewBox="100 100 824 824"|' icon.svg | \
#     rsvg-convert -w 64 -h 64 -o ../../internal/app/favicon.png
import json, sys
leaf = json.load(open("leaf.json"))
sq = open("squircle.d").read().strip()

def icon():
    tile = '''<linearGradient id="tile" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#FFFFFF"/><stop offset="1" stop-color="#EEF0EA"/></linearGradient>'''
    lines = "".join(f'<path d="M150 {y} H874" stroke="#D8E1EE" stroke-width="5"/>' for y in range(300, 860, 92))
    paper = f'<g clip-path="url(#clip)">{lines}<path d="M262 100 V924" stroke="#F4C7C3" stroke-width="5"/></g>'
    light, deep, vein, rim, shadow = "#B6E36A", "#2E8B47", "#F3FFD9", "#1F6B35", "#264d1c"
    glow = "#FFF6B0"
    veins = "".join(f'<path d="{d}"/>' for d in leaf["veins"])
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024">
<defs>
  {tile}
  <radialGradient id="gloss" cx=".28" cy=".16" r=".8">
    <stop offset="0" stop-color="#fff" stop-opacity=".30"/><stop offset="1" stop-color="#fff" stop-opacity="0"/></radialGradient>
  <linearGradient id="rim" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#fff" stop-opacity=".6"/><stop offset=".1" stop-color="#fff" stop-opacity="0"/>
    <stop offset=".92" stop-color="#000" stop-opacity="0"/><stop offset="1" stop-color="#000" stop-opacity=".18"/></linearGradient>
  <linearGradient id="body" gradientUnits="userSpaceOnUse" x1="-200" y1="-300" x2="200" y2="300">
    <stop offset="0" stop-color="{light}"/><stop offset="1" stop-color="{deep}"/></linearGradient>
  <radialGradient id="backlight" gradientUnits="userSpaceOnUse" cx="-40" cy="-40" r="300">
    <stop offset="0" stop-color="{glow}" stop-opacity=".45"/><stop offset="1" stop-color="{glow}" stop-opacity="0"/></radialGradient>
  <radialGradient id="veinfade" gradientUnits="userSpaceOnUse" cx="0" cy="0" r="250">
    <stop offset="0" stop-color="{vein}" stop-opacity=".95"/><stop offset=".75" stop-color="{vein}" stop-opacity=".55"/>
    <stop offset="1" stop-color="{vein}" stop-opacity=".05"/></radialGradient>
  <linearGradient id="ribfade" gradientUnits="userSpaceOnUse" x1="0" y1="300" x2="30" y2="-318">
    <stop offset="0" stop-color="{vein}" stop-opacity="1"/><stop offset="1" stop-color="{vein}" stop-opacity=".25"/></linearGradient>
  <linearGradient id="stalk" gradientUnits="userSpaceOnUse" x1="0" y1="280" x2="-40" y2="420">
    <stop offset="0" stop-color="{deep}"/><stop offset="1" stop-color="{rim}"/></linearGradient>
  <filter id="tileShadow" x="-10%" y="-10%" width="120%" height="125%"><feGaussianBlur stdDeviation="12"/></filter>
  <filter id="soft" x="-40%" y="-40%" width="180%" height="180%"><feGaussianBlur stdDeviation="22"/></filter>
  <filter id="close" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="6"/></filter>
  <clipPath id="clip"><path d="{sq}"/></clipPath>
  <clipPath id="leafclip"><path d="{leaf["outline"]}"/></clipPath>
</defs>
<path d="{sq}" transform="translate(0 12)" fill="#000" opacity=".28" filter="url(#tileShadow)"/>
<path d="{sq}" fill="url(#tile)"/>
{paper}
<g clip-path="url(#clip)">
  <g transform="translate(540 566) rotate(38) scale(1.02)" opacity=".30" filter="url(#soft)"><path d="{leaf["outline"]}" fill="{shadow}"/></g>
  <g transform="translate(520 538) rotate(38) scale(1.02)" opacity=".28" filter="url(#close)"><path d="{leaf["outline"]}" fill="{shadow}"/></g>
  <g transform="translate(512 526) rotate(38) scale(1.02)">
    <path d="M0 290 C 6 340 -6 380 -34 418" stroke="url(#stalk)" stroke-width="20" stroke-linecap="round" fill="none"/>
    <path d="{leaf["outline"]}" fill="url(#body)"/>
    <path d="{leaf["outline"]}" fill="url(#backlight)"/>
    <g clip-path="url(#leafclip)">
      <path d="{leaf["right"]}" fill="#000" opacity=".10"/>
      <g stroke="url(#veinfade)" stroke-width="7" stroke-linecap="round" fill="none">{veins}</g>
      <path d="{leaf["midrib"]}" stroke="url(#ribfade)" stroke-width="11" stroke-linecap="round" fill="none"/>
    </g>
    <path d="{leaf["outline"]}" fill="none" stroke="{rim}" stroke-opacity=".35" stroke-width="3"/>
    <path d="M-10 286 C-118 248 -226 130 -222 -24 C-216 -168 -120 -276 30 -326" stroke="#fff" stroke-opacity=".7" stroke-width="5" stroke-linecap="round" fill="none"/>
  </g>
</g>
<path d="{sq}" fill="none" stroke="url(#rim)" stroke-width="4"/>
</svg>'''

open("icon.svg", "w").write(icon())
