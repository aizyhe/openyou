#!/usr/bin/env python3
"""
Merge web/ sources into one index.html — the daemon //go:embeds a single file.
Logo stays single-sourced from web/logo.svg: inlined as <symbol> for the page
AND regenerated into the Android launcher/notification vectors on every build.
"""
import json, sys, os, re
from pathlib import Path

ROOT = Path(__file__).parent.parent
MANIFEST = Path(__file__).parent / "manifest.json"
LOGO_SVG = Path(__file__).parent / "logo.svg"
ANDROID_VECTOR = ROOT / "android/app/src/main/res/drawable/ic_launcher_foreground.xml"
ANDROID_NOTIF_ICON = ROOT / "android/app/src/main/res/drawable/ic_notification.xml"

def _svg_root(svg_text):
    m = re.search(r"<svg\b([^>]*)>(.*)</svg>", svg_text, re.S)
    if not m:
        raise SystemExit("logo svg: malformed root element")
    # Strip comments — they may mention "<path>" and would confuse the element parsers below.
    return m.group(1), re.sub(r"<!--.*?-->", "", m.group(2), flags=re.S)

def logo_symbol(svg_text, sym_id):
    """Wrap root <svg> in <symbol id=...> so <use href="#id"> works — one design everywhere."""
    attrs, inner = _svg_root(svg_text)
    attrs = re.sub(r'\s(?:width|height)="[^"]*"', "", attrs)
    return ('<svg xmlns="http://www.w3.org/2000/svg" style="position:absolute;width:0;height:0" aria-hidden="true">'
            f'<symbol id="{sym_id}"{attrs}>{inner}</symbol></svg>')

def _grad_angle(ga):
    x1, y1 = float(ga.get("x1", 0)), float(ga.get("y1", 0))
    x2, y2 = float(ga.get("x2", 1)), float(ga.get("y2", 0))
    if abs(x2 - x1) >= abs(y2 - y1):
        return 0 if x2 > x1 else 180
    return 270 if y2 > y1 else 90

def logo_to_android_vector():
    """Regenerate the Android foreground from logo.svg — subset kept small so one file drives both platforms."""
    attrs, inner = _svg_root(LOGO_SVG.read_text())
    vb = re.search(r'viewBox="([\d.\s]+)"', attrs).group(1).split()
    vw, vh = vb[2], vb[3]
    grads = {}
    for gm in re.finditer(r"<linearGradient\b([^>]*)>(.*?)</linearGradient>", inner, re.S):
        ga = dict(re.findall(r'([\w-]+)="([^"]*)"', gm.group(1)))
        colors = [dict(re.findall(r'([\w-]+)="([^"]*)"', sa)).get("stop-color", "#000000")
                  for sa in re.findall(r"<stop\b([^>]*)/?>", gm.group(2))]
        if ga.get("id") and colors:
            grads[ga["id"]] = (ga, colors)
    paths = []
    for pm in re.finditer(r"<path\b([^>]*)/?>", inner):
        a = dict(re.findall(r'([\w-]+)="([^"]*)"', pm.group(1)))
        line = f'  <path android:pathData="{a["d"]}"'
        fill = a.get("fill", "#000000")
        gm = re.match(r"url\(#([^)]+)\)", fill)
        if gm and gm.group(1) in grads:
            ga, colors = grads[gm.group(1)]
            line += '>\n    <aapt:attr name="android:fillColor">\n'
            line += (f'      <gradient android:startColor="{colors[0]}" android:endColor="{colors[-1]}"'
                     f' android:type="linear" android:angle="{_grad_angle(ga)}"/>\n')
            line += "    </aapt:attr>\n  </path>"
        else:
            line += ' android:fillColor="#00000000"' if fill == "none" else f' android:fillColor="{fill}"'
            stroke = a.get("stroke")
            if stroke and stroke != "none":
                line += f' android:strokeColor="{stroke}"'
                if "stroke-width" in a:
                    line += f' android:strokeWidth="{a["stroke-width"]}"'
                if a.get("stroke-linecap") == "round":
                    line += ' android:strokeLineCap="round"'
            line += "/>"
        paths.append(line)
    xml = ('<vector xmlns:android="http://schemas.android.com/apk/res/android"\n'
           '    xmlns:aapt="http://schemas.android.com/aapt"\n'
           f'    android:width="108dp" android:height="108dp" android:viewportWidth="{vw}" android:viewportHeight="{vh}">\n'
           '  <!-- AUTO-GENERATED from web/logo.svg by web/build.py — edit the SVG, never this file. -->\n'
           + "\n".join(paths) + "\n</vector>\n")
    ANDROID_VECTOR.write_text(xml)
    logo_to_notification_icon()

def logo_to_notification_icon():
    """Monochrome white strokes only — Android tints small icons itself, so filled plates are dropped."""
    attrs, inner = _svg_root(LOGO_SVG.read_text())
    vb = re.search(r'viewBox="([\d.\s]+)"', attrs).group(1).split()
    vw, vh = vb[2], vb[3]
    paths = []
    for pm in re.finditer(r"<path\b([^>]*)/?>", inner):
        a = dict(re.findall(r'([\w-]+)="([^"]*)"', pm.group(1)))
        stroke = a.get("stroke")
        if not stroke or stroke == "none":
            continue  # filled plates don't belong in a monochrome small icon
        paths.append(f'  <path android:pathData="{a["d"]}" android:fillColor="#00000000"'
                     f' android:strokeColor="#FFFFFF"'
                     f' android:strokeWidth="{a.get("stroke-width", 2)}"'
                     + (' android:strokeLineCap="round"' if a.get("stroke-linecap") == "round" else "") + "/>")
    if paths:
        xml = ('<vector xmlns:android="http://schemas.android.com/apk/res/android"\n'
               f'    android:width="24dp" android:height="24dp" android:viewportWidth="{vw}" android:viewportHeight="{vh}">\n'
               '  <!-- AUTO-GENERATED from web/logo.svg by web/build.py — edit the SVG, never this file. -->\n'
               + "\n".join(paths) + "\n</vector>\n")
        ANDROID_NOTIF_ICON.write_text(xml)

def main():
    version = date = None
    args = sys.argv[1:]
    for i, a in enumerate(args):
        if a == "--version" and i+1 < len(args): version = args[i+1]
        if a == "--date"    and i+1 < len(args): date    = args[i+1]

    m = json.loads(MANIFEST.read_text())
    out_path = ROOT / m["output"]
    parts = []

    for part in m["parts"]:
        t = part["type"]
        if t == "html":
            parts.append((ROOT / part["file"]).read_text())
        elif t == "css":
            css = (ROOT / part["file"]).read_text()
            parts.append(f"<style>\n{css}\n</style>\n")
        elif t == "svg":
            # Logo inlined once as a <symbol>, used via <use href="#id"> — lives in exactly one file (web/logo.svg).
            parts.append(logo_symbol((ROOT / part["file"]).read_text(), part["id"]))
        elif t == "js":
            js_chunks = []
            for f in part["files"]:
                js_chunks.append(f"// ---- {f} ----")
                js_chunks.append((ROOT / f).read_text())
            parts.append("<script>\n(function() {\n" + "\n".join(js_chunks) + "\n})();\n</script>\n")

    html = "".join(parts)

    # Same version/date tokens build.sh sed-replaces — source and artifact must stay in sync.
    if version:
        html = re.sub(r"APP_VERSION = '[^']*'", f"APP_VERSION = '{version}'", html)
    if date:
        html = re.sub(r"BUILD_DATE = '[^']*'", f"BUILD_DATE = '{date}'", html)

    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(html)
    print(f"Built {out_path} ({len(html.splitlines())} lines)")

    if LOGO_SVG.exists():
        logo_to_android_vector()
        print(f"Logo synced -> {ANDROID_VECTOR.relative_to(ROOT)}")

if __name__ == "__main__":
    main()
