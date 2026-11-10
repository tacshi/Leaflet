import json, sys
leaf = json.load(open("leaf.json"))
squircle = open("squircle.d").read().strip()

def icon(ios=False, dark=False):
    """The macOS icon, or with ios the full square iOS rounds, in light or
    dark paper."""
    sq = "M100 100 H924 V924 H100 Z" if ios else squircle
    top, bottom, ruled, margin = ("#2B2F33", "#1B1D20", "#3A4452", "#7A4A46") if dark else ("#FFFFFF", "#EEF0EA", "#D8E1EE", "#F4C7C3")
    tile = f'''<linearGradient id="tile" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="{top}"/><stop offset="1" stop-color="{bottom}"/></linearGradient>'''
    lines = "".join(f'<path d="M150 {y} H874" stroke="{ruled}" stroke-width="5"/>' for y in range(300, 860, 92))
    paper = f'<g clip-path="url(#clip)">{lines}<path d="M262 100 V924" stroke="{margin}" stroke-width="5"/></g>'
    light, deep, vein, rim, shadow = "#B6E36A", "#2E8B47", "#F3FFD9", "#1F6B35", "#264d1c"
    glow = "#FFF6B0"
    veins = "".join(f'<path d="{d}"/>' for d in leaf["veins"])
    view = "100 100 824 824" if ios else "0 0 1024 1024"
    under = "" if ios else f'<path d="{sq}" transform="translate(0 12)" fill="#000" opacity=".28" filter="url(#tileShadow)"/>\n'
    edge = "" if ios else f'\n<path d="{sq}" fill="none" stroke="url(#rim)" stroke-width="4"/>'
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="{view}">
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
{under}<path d="{sq}" fill="url(#tile)"/>
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
</g>{edge}
</svg>'''

open("icon.svg", "w").write(icon())
open("ios-icon.svg", "w").write(icon(ios=True))
open("ios-icon-dark.svg", "w").write(icon(ios=True, dark=True))
