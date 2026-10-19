#!/usr/bin/env python3
"""Add a read-only ROOM tab to the StudioLive Open Stage Control session.

Text widgets are display-only (interaction false, bypass true) and show strings
that jive-room and the bridge publish under /dsp/ui/. Sending widgets:
- APPLY / UNDO (double-tap): /dsp/apply and /dsp/revert to the bridge, which
  validates, snapshots, writes, and verifies;
- daemon commands (/dsp/cmd/...), which the bridge forwards only to the
  jive-room daemon: channel select, monitor, captures, PA ARM and CONFIRM
  (double-tap), session, analysis, and profile save/restore. None of these
  writes to the desk; a profile restore only becomes a pending proposal;
- automatic cuts: ENABLE (double-tap) and AUTO OFF, /dsp/auto/enable and
  /dsp/auto/disable to the bridge, which alone can enable them (and only when
  its dsp-config.json allows it).
Re-running replaces an existing ROOM tab.

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


def find_widget(node, pred):
    if pred(node):
        return node
    for child in node.get("widgets", []) + node.get("tabs", []):
        found = find_widget(child, pred)
        if found:
            return found
    return None


def find_text_template(node):
    return find_widget(node, lambda w: w.get("type") == "text")


def button(template, wid, address, label, left, top, colour, on=1, double=True, width=180, height=44):
    """A tap-mode button that sends <address> <on> then 0 to the bridge.

    Receivers act only on the press (>= 0.5, or the channel number). Desk
    writes still happen only through /dsp/apply, which the bridge validates,
    snapshots, and verifies."""
    w = copy.deepcopy(template)
    w.update(
        {
            "id": wid, "address": address, "label": label, "left": left, "top": top,
            "width": width, "height": height, "value": 0, "default": 0, "mode": "tap",
            "on": on, "off": 0, "doubleTap": double, "bypass": False, "interaction": True,
            "onTouch": "", "onValue": "", "onCreate": "", "preArgs": "", "typeTags": "",
            "target": "", "colorFill": colour, "css": "font-weight:700;",
        }
    )
    return w


def daemon_controls(template, btn):
    """Right-hand column: commands for the jive-room daemon (/dsp/cmd/...)."""
    x, w = 800, 380
    out = [
        text(template, "room_daemon", "/dsp/ui/room/daemon", 12, 38, "font-size:11px;", "jive-room daemon not running"),
        text(template, "room_ch_label", "/ui/room_ch_label", 54, 20, "font-size:11px;opacity:0.8;", "CHANNEL"),
    ]
    for n in range(1, 13):
        col, row = (n - 1) % 6, (n - 1) // 6
        out.append(button(btn, f"room_ch{n}", "/dsp/cmd/channel", str(n), x + col * 63, 76 + row * 42,
                          "#37474f", on=n, double=False, width=58, height=36))
    rows = [
        (170, "MONITOR", [("room_mon_start", "/dsp/cmd/monitor/start", "START", "#1565c0", False),
                          ("room_mon_stop", "/dsp/cmd/monitor/stop", "STOP", "#455a64", False)]),
        (240, "CAPTURE", [("room_cap_empty", "/dsp/cmd/capture/empty", "EMPTY", "#00695c", False),
                          ("room_cap_occupied", "/dsp/cmd/capture/occupied", "OCCUPIED", "#00695c", False),
                          ("room_cap_presenter", "/dsp/cmd/capture/presenter", "PRESENTER", "#00695c", False)]),
        (290, "", [("room_cap_verify", "/dsp/cmd/capture/verification", "VERIFY", "#00695c", False),
                   ("room_task_stop", "/dsp/cmd/stop", "STOP TASK", "#b71c1c", False)]),
        (360, "PA MEASUREMENT (plays pink noise)", [("room_pa_arm", "/dsp/cmd/pa/arm", "ARM", "#ef6c00", False),
                                                    ("room_pa_confirm", "/dsp/cmd/pa/confirm", "CONFIRM (double-tap)", "#b71c1c", True)]),
        (430, "SESSION", [("room_session_new", "/dsp/cmd/session/new", "NEW", "#455a64", False),
                          ("room_analyse", "/dsp/cmd/analyse", "ANALYSE", "#1565c0", False)]),
        (500, "PROFILE", [("room_profile_save", "/dsp/cmd/profile/save", "SAVE", "#455a64", False),
                          ("room_profile_restore", "/dsp/cmd/profile/submit", "RESTORE (pending)", "#455a64", False)]),
        (580, "AUTOMATIC CUTS (needs config + measured calibration; monitor must run)",
         [("room_auto_enable", "/dsp/auto/enable", "ENABLE (double-tap)", "#6a1b9a", True),
          ("room_auto_off", "/dsp/auto/disable", "AUTO OFF", "#b71c1c", False)]),
    ]
    for top, label, buttons in rows:
        if label:
            out.append(text(template, f"room_lbl_{top}", f"/ui/room_lbl_{top}", top - 22, 20,
                            "font-size:11px;opacity:0.8;", label))
        bw = (w - 10 * (len(buttons) - 1)) // len(buttons)
        for i, (wid, addr, lab, colour, dbl) in enumerate(buttons):
            out.append(button(btn, wid, addr, lab, x + i * (bw + 10), top, colour, double=dbl, width=bw, height=40))
    out += [
        text(template, "room_auto_status", "/dsp/ui/auto/status", 628, 30, "font-size:11px;font-weight:600;",
             "automatic cuts: state unknown (bridge not reporting)"),
        text(template, "room_auto_last", "/dsp/ui/auto/last", 660, 44, "font-size:11px;"),
    ]
    for w_ in out:
        if w_["type"] == "text":
            w_["left"], w_["width"] = x, w
    return out


def lower_panels(template, top):
    """Live advice and session results below the ring-out and apply panels."""
    out = [text(template, "room_live_status", "/dsp/ui/live/status", top, 30, "font-size:12px;font-weight:600;",
                "live advice: monitor not running")]
    for i in range(1, 6):
        out.append(text(template, f"room_live_{i}", f"/dsp/ui/live/{i}", top + 6 + i * 32, 30, "font-size:11px;"))
    top += 6 + 6 * 32 + 12
    out.append(text(template, "room_results_label", "/ui/room_results_label", top, 26, "font-size:12px;font-weight:600;",
                    "SESSION ANALYSIS"))
    for i in range(1, 7):
        out.append(text(template, f"room_result_{i}", f"/dsp/ui/room/result/{i}", top + i * 30, 28, "font-size:11px;"))
    return out


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
        text(template, "room_stage", "/dsp/ui/room/stage", 162, 28, "font-size:12px;font-style:italic;"),
    ]
    top = 198
    for i in range(1, 5):
        widgets.append(text(template, f"room_candidate_{i}", f"/dsp/ui/ringout/candidate/{i}", top, 32,
                            "font-size:13px;font-weight:600;"))
        widgets.append(text(template, f"room_proposal_{i}", f"/dsp/ui/ringout/proposal/{i}", top + 34, 44,
                            "font-size:11px;"))
        top += 92

    btn_template = find_widget(session["content"], lambda w: w.get("id") == "record_stop_btn")
    widgets += [
        text(template, "room_pending", "/dsp/ui/proposal/summary", top, 30, "font-size:12px;font-weight:600;",
             "no pending proposal"),
        text(template, "room_apply_status", "/dsp/ui/apply/status", top + 32, 28, "font-size:11px;"),
        button(btn_template, "room_apply_btn", "/dsp/apply", "APPLY (double-tap)", LEFT, top + 66, "#2e7d32"),
        button(btn_template, "room_undo_btn", "/dsp/revert", "UNDO (double-tap)", LEFT + 200, top + 66, "#455a64"),
    ]
    widgets += daemon_controls(template, btn_template)
    widgets += lower_panels(template, top + 126)

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
