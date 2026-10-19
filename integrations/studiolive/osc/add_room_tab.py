#!/usr/bin/env python3
"""Add a read-only ROOM tab to the StudioLive Open Stage Control session.

Every ROOM widget is display-only (interaction false, bypass true), so it never
sends OSC. It shows strings that jive-room publishes under /dsp/ui/ringout/
through the bridge's DSP port. Re-running replaces an existing ROOM tab.

Usage: add_room_tab.py <input session.json> <output session.json>
"""

import copy
import json
import sys

TAB_ID = "room"
WIDTH = 760
LEFT = 20


def text(template, wid, address, top, height, css, value=""):
    w = copy.deepcopy(template)
    w.update(
        {
            "id": wid,
            "address": address,
            "value": value,
            "default": value,
            "top": top,
            "left": LEFT,
            "width": WIDTH,
            "height": height,
            "css": css,
            "interaction": False,
            "bypass": True,
            "label": False,
            "preArgs": "",
            "typeTags": "",
            "target": "",
            "align": "left",
        }
    )
    return w


def find_text_template(node):
    if node.get("type") == "text":
        return node
    for child in node.get("widgets", []) + node.get("tabs", []):
        found = find_text_template(child)
        if found:
            return found
    return None


def main(src, dst):
    with open(src) as f:
        session = json.load(f)
    root = session["content"]
    tabs = [t for t in root["tabs"] if t.get("id") != TAB_ID]
    meters = next(t for t in tabs if t.get("id") == "meters")
    template = find_text_template(meters)

    widgets = [
        text(template, "room_title", "/ui/room_title", 12, 38,
             "font-size:15px;font-weight:600;", "ROOM · RING-OUT ASSISTANT (read-only)"),
        text(template, "room_note", "/ui/room_note", 52, 44, "font-size:11px;opacity:0.8;",
             "jive-room proposes bounded cuts; nothing here writes to the desk. "
             "Raise gain yourself, slowly, and stop as soon as anything rings."),
        text(template, "room_status", "/dsp/ui/ringout/status", 104, 30, "font-size:12px;"),
        text(template, "room_updated", "/dsp/ui/ringout/updated", 136, 24, "font-size:10px;opacity:0.7;"),
    ]
    top = 172
    for i in range(1, 5):
        widgets.append(text(template, f"room_candidate_{i}", f"/dsp/ui/ringout/candidate/{i}", top, 32,
                            "font-size:13px;font-weight:600;"))
        widgets.append(text(template, f"room_proposal_{i}", f"/dsp/ui/ringout/proposal/{i}", top + 34, 44,
                            "font-size:11px;"))
        top += 92

    tab = copy.deepcopy({k: v for k, v in meters.items() if k not in ("widgets", "tabs")})
    tab.update({"id": TAB_ID, "label": "ROOM", "widgets": widgets, "tabs": []})
    tabs.append(tab)
    root["tabs"] = tabs

    with open(dst, "w") as f:
        json.dump(session, f, indent=2)
        f.write("\n")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
