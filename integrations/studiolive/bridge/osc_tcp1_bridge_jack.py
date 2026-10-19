#!/usr/bin/env python3
"""
StudioLive permanent OSC bridge for the Mac mini.

Functions:
- Open Stage Control UDP 9000 <-> osclive OSC/TCP 1.0 on 4301
- Mixer-state cache and /bridge/resync
- Starts/stops JACK RTA
- Starts/stops 16-channel JACK recorder
- Publishes recorder/RTA status to Open Stage Control UDP 9001

Local commands (never sent to osclive):
  /bridge/resync
  /analyser/start
  /analyser/stop
  /analyser/toggle
  /analyser/status
  /analyser/select <1..12>
  /recording/start
  /recording/stop
  /recording/status
  /dsp/...          (reserved for jive-room; never forwarded to osclive)

DSP state port (127.0.0.1:9002, read-only towards the mixer):
  /dsp/subscribe <port>    register 127.0.0.1:<port>, replay the state cache,
                           then stream every osclive packet; renew within
                           DSP_SUBSCRIBER_TTL seconds or the subscription lapses
  /dsp/unsubscribe <port>
  /dsp/ui/...              forwarded to Open Stage Control only
  /dsp/proposal/set <json> validate a jive-room-proposal/v1 and hold it pending
  /dsp/proposal/clear
  /dsp/apply <id>          apply the pending proposal with that id
  /dsp/revert              undo the most recent apply
Anything else received on the DSP port is dropped. Nothing from it is ever
forwarded to osclive as-is.

Apply engine (the only path that writes to the desk on behalf of jive-room):
  Proposals are allow-listed address/raw pairs (channel HPF/EQ, GEQ bands).
  On apply the bridge checks osclive is connected, that target channels have
  been quiet on the desk for APPLY_SETTLE_SECONDS, and (for ring-out
  proposals) that gains only go down by at most RINGOUT_MAX_CUT_RAW and that
  no boosting band is retuned. It snapshots current values, writes the
  differences at APPLY_WRITE_GAP intervals, verifies read-back to the exact
  raw step, pushes the snapshot onto a persisted undo stack, and writes an
  audit line. It never writes in response to incoming state, and does
  nothing to the desk when a client disconnects.
  Open Stage Control may send /dsp/apply and /dsp/revert (button press >= 0.5).
  Results go to the UI (/dsp/ui/apply/status) and to DSP subscribers
  (/dsp/result <json>).
"""

import json
import os
import re
import sys
import socket
import struct
import threading
import time
import signal
import subprocess
from collections import OrderedDict
from datetime import datetime

from pythonosc.osc_packet import OscPacket
from pythonosc.osc_message_builder import OscMessageBuilder

UDP_LISTEN_HOST = "0.0.0.0"
UDP_LISTEN_PORT = int(os.environ.get("STUDIOLIVE_UI_LISTEN_PORT", "9000"))
OSC_UI_HOST = "127.0.0.1"
OSC_UI_PORT = int(os.environ.get("STUDIOLIVE_UI_PORT", "9001"))

OSCLIVE_HOST = "127.0.0.1"
OSCLIVE_PORT = int(os.environ.get("STUDIOLIVE_OSCLIVE_PORT", "4301"))
RECONNECT_DELAY = 2.0
MAX_FRAME = 1024 * 1024

DSP_LISTEN_HOST = "127.0.0.1"
DSP_LISTEN_PORT = int(os.environ.get("STUDIOLIVE_DSP_PORT", "9002"))
DSP_SUBSCRIBER_TTL = 15.0
DSP_MAX_SUBSCRIBERS = 4
DSP_STATE_DIR = os.path.expanduser(os.environ.get("STUDIOLIVE_DSP_STATE_DIR", "~/studiolive"))

PROPOSAL_SCHEMA = "jive-room-proposal/v1"
RESULT_SCHEMA = "jive-room-apply-result/v1"
PROPOSAL_SOURCES = ("ringout", "profile")
MAX_PROPOSAL_CHANGES = 256
# A target channel must have been quiet on the desk this long, so a write
# (which rewrites osclive's whole cached block) cannot undo a fresh knob move.
APPLY_SETTLE_SECONDS = 1.0
APPLY_MIN_INTERVAL = 1.0
APPLY_WRITE_GAP = 0.05
APPLY_VERIFY_TIMEOUT = 3.0
# One ring-out step: 26 raw steps is about 3 dB on the ±15 dB channel EQ
# model. GEQ dB per step is uncalibrated; the same raw bound applies.
RINGOUT_MAX_CUT_RAW = 26
EQ_FLAT_RAW = 128
UNDO_DEPTH = 20
AUDIT_MAX_BYTES = 1024 * 1024

CHANNEL_SWITCHES = ("hpf", "eqlow", "eqmid", "eqhigh", "eqlowshelf", "eqmidhiq", "eqhighshelf")
CHANNEL_FREQS = ("hpffreq", "eqlowfreq", "eqmidfreq", "eqhighfreq")
CHANNEL_GAINS = ("eqlowgain", "eqmidgain", "eqhighgain")
RINGOUT_CHANNEL_CONTROLS = ("eqmid", "eqmidhiq", "eqmidfreq", "eqmidgain")
_CHANNEL_RE = re.compile(r"^/channel/(ch(?:[1-9]|1[0-2]))/(%s)$" % "|".join(CHANNEL_SWITCHES + CHANNEL_FREQS + CHANNEL_GAINS))
_GEQ_RE = re.compile(r"^/channel/geq0/([1-9]|[12][0-9]|3[01])$")
_ID_RE = re.compile(r"^[A-Za-z0-9._-]{1,64}$")
NON_CONTROL_SUFFIXES = ("/level", "/peak", "/label")

ANALYSER_SCRIPT = os.environ.get(
    "STUDIOLIVE_ANALYSER",
    os.path.expanduser("~/studiolive_spectrum_jack.py"),
)
RECORDER_SCRIPT = os.environ.get(
    "STUDIOLIVE_RECORDER",
    os.path.expanduser("~/studiolive_jack_recorder.py"),
)
RECORD_DIR = os.path.expanduser(
    os.environ.get("STUDIOLIVE_RECORD_DIR", "~/Recordings/StudioLive")
)

stop_event = threading.Event()

tcp_lock = threading.Lock()
tcp_sock = None

cache_lock = threading.Lock()
state_cache = OrderedDict()
state_values = {}  # address -> first OSC argument, from osclive
channel_changed = {}  # "ch3" / "geq0" -> monotonic time of last control update

apply_lock = threading.Lock()  # one apply/revert at a time
pending_lock = threading.Lock()
pending_proposal = None
undo_stack = []
last_apply_time = 0.0

proc_lock = threading.Lock()
analyser_proc = None
analyser_error = ""
analyser_stop_requested = False
selected_channel = 1

recorder_proc = None
recording_error = ""
recording_file = ""
recording_started = 0.0
recording_stop_requested = False

dsp_lock = threading.Lock()
dsp_subscribers = {}  # (host, port) -> monotonic expiry

udp_out = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)


def log(msg):
    print(time.strftime("[%H:%M:%S]"), msg, flush=True)


def build_osc(addr, value):
    b = OscMessageBuilder(address=addr)
    if isinstance(value, str):
        b.add_arg(value)
    elif isinstance(value, int):
        b.add_arg(value)
    else:
        b.add_arg(float(value))
    return b.build().dgram


def ui_send(addr, value):
    try:
        udp_out.sendto(build_osc(addr, value), (OSC_UI_HOST, OSC_UI_PORT))
    except OSError as e:
        log(f"UI send error: {e}")


def osc_address(data):
    if not data or data.startswith(b"#bundle"):
        return None
    end = data.find(b"\0")
    if end <= 0:
        return None
    try:
        addr = data[:end].decode("utf-8")
    except UnicodeDecodeError:
        return None
    return addr if addr.startswith("/") else None


def osc_first_param(data):
    try:
        packet = OscPacket(data)
        for timed in packet.messages:
            p = timed.message.params
            if p:
                return p[0]
    except Exception:
        pass
    return None


def track_state(addr, params):
    """Caller holds cache_lock. Records values and per-channel change times."""
    if not params:
        return
    state_values[addr] = params[0]
    if addr.startswith("/channel/") and not addr.endswith(NON_CONTROL_SUFFIXES):
        parts = addr.split("/")
        if len(parts) == 4:
            channel_changed[parts[2]] = time.monotonic()


def cache_packet(data):
    try:
        packet = OscPacket(data)
        for timed in packet.messages:
            msg = timed.message
            if msg.address and msg.dgram:
                with cache_lock:
                    state_cache[msg.address] = msg.dgram
                    state_cache.move_to_end(msg.address)
                    track_state(msg.address, msg.params)
    except Exception:
        addr = osc_address(data)
        if addr:
            with cache_lock:
                state_cache[addr] = data
                state_cache.move_to_end(addr)


def replay_cache():
    with cache_lock:
        packets = list(state_cache.values())
    for pkt in packets:
        try:
            udp_out.sendto(pkt, (OSC_UI_HOST, OSC_UI_PORT))
        except OSError as e:
            log(f"UDP replay error: {e}")
            return
    log(f"Replayed {len(packets)} cached mixer states")


def dsp_live_subscribers():
    now = time.monotonic()
    with dsp_lock:
        for addr in [a for a, exp in dsp_subscribers.items() if exp <= now]:
            del dsp_subscribers[addr]
            log(f"DSP subscriber {addr[0]}:{addr[1]} expired")
        return list(dsp_subscribers)


def dsp_forward(payload):
    for addr in dsp_live_subscribers():
        try:
            udp_out.sendto(payload, addr)
        except OSError:
            pass


def dsp_replay(addr):
    with cache_lock:
        packets = list(state_cache.values())
    for pkt in packets:
        try:
            udp_out.sendto(pkt, addr)
        except OSError as e:
            log(f"DSP replay error: {e}")
            return
    try:
        udp_out.sendto(build_osc("/dsp/replay_done", len(packets)), addr)
    except OSError:
        pass
    log(f"Replayed {len(packets)} cached mixer states to DSP {addr[0]}:{addr[1]}")


def control_kind(addr):
    """Return (kind, channel, name) for an allow-listed control, else None.

    kind is "switch", "freq", or "gain"; GEQ bands are gains."""
    m = _CHANNEL_RE.match(addr)
    if m:
        name = m.group(2)
        kind = "switch" if name in CHANNEL_SWITCHES else "freq" if name in CHANNEL_FREQS else "gain"
        return kind, m.group(1), name
    m = _GEQ_RE.match(addr)
    if m:
        return "gain", "geq0", m.group(1)
    return None


def value_to_raw(value, kind):
    v = max(0.0, min(1.0, float(value)))
    if kind == "switch":
        return 1 if v >= 0.5 else 0
    return int(v * 255 + 1e-6)  # osclive stores int(v*255)


def raw_to_wire(raw, kind):
    # Mid-step for continuous controls leaves half a step of margin.
    return float(raw) if kind == "switch" else (raw + 0.5) / 255


def current_raw(addr, kind):
    with cache_lock:
        v = state_values.get(addr)
    if v is None:
        return None
    try:
        return value_to_raw(v, kind)
    except (TypeError, ValueError):
        return None


def validate_proposal(p):
    """Structural checks at submit time. Returns (proposal, reason)."""
    if not isinstance(p, dict) or p.get("schema_version") != PROPOSAL_SCHEMA:
        return None, f"schema_version must be {PROPOSAL_SCHEMA}"
    pid, source = p.get("id"), p.get("source")
    if not isinstance(pid, str) or not _ID_RE.match(pid):
        return None, "id must be 1-64 characters of [A-Za-z0-9._-]"
    if source not in PROPOSAL_SOURCES:
        return None, f"source must be one of {', '.join(PROPOSAL_SOURCES)}"
    changes = p.get("changes")
    if not isinstance(changes, list) or not 0 < len(changes) <= MAX_PROPOSAL_CHANGES:
        return None, f"changes must list 1-{MAX_PROPOSAL_CHANGES} controls"
    seen, norm = set(), []
    for c in changes:
        addr = c.get("address") if isinstance(c, dict) else None
        raw = c.get("raw") if isinstance(c, dict) else None
        kinfo = control_kind(addr) if isinstance(addr, str) else None
        if kinfo is None:
            return None, f"{addr!r} is not an allow-listed HPF/EQ/GEQ control"
        if addr in seen:
            return None, f"{addr} appears twice"
        seen.add(addr)
        if not isinstance(raw, int) or isinstance(raw, bool) or not 0 <= raw <= 255:
            return None, f"{addr}: raw must be an integer 0-255"
        if kinfo[0] == "switch" and raw not in (0, 1):
            return None, f"{addr}: a switch takes raw 0 or 1"
        if source == "ringout" and not (kinfo[1] == "geq0" or kinfo[2] in RINGOUT_CHANNEL_CONTROLS):
            return None, f"{addr}: ring-out proposals may only touch the mid band or GEQ bands"
        norm.append({"address": addr, "raw": raw, "kind": kinfo[0], "channel": kinfo[1], "name": kinfo[2]})
    summary = p.get("summary") if isinstance(p.get("summary"), str) else ""
    return {"id": pid, "source": source, "summary": summary[:200], "changes": norm}, None


def ringout_policy(changes):
    """Apply-time policy for ring-out proposals: cuts only, bounded, no boost by retune."""
    for c in changes:
        cur = current_raw(c["address"], c["kind"])
        if c["kind"] == "gain":
            if cur is None:
                return f"{c['address']}: current value unknown"
            if c["raw"] > cur:
                return f"{c['address']}: ring-out proposals may only cut (raw {cur} -> {c['raw']})"
            if cur - c["raw"] > RINGOUT_MAX_CUT_RAW:
                return f"{c['address']}: cut of {cur - c['raw']} raw steps exceeds {RINGOUT_MAX_CUT_RAW}"
    for c in changes:
        if c["name"] == "eqmidfreq":
            gain_addr = f"/channel/{c['channel']}/eqmidgain"
            g = next((x["raw"] for x in changes if x["address"] == gain_addr), None)
            if g is None:
                g = current_raw(gain_addr, "gain")
            if g is None or g > EQ_FLAT_RAW:
                return f"{c['channel']}: retuning the mid band while it boosts (raw {g}) is not allowed"
    return None


def settle_check(changes):
    now = time.monotonic()
    with cache_lock:
        for ch in sorted({c["channel"] for c in changes}):
            t = channel_changed.get(ch)
            if t is not None and now - t < APPLY_SETTLE_SECONDS:
                return f"{ch} changed {now - t:.1f} s ago; apply when the desk is quiet"
    return None


def state_path(name):
    return os.path.join(DSP_STATE_DIR, name)


def persist_undo():
    path = state_path("dsp-undo.json")
    tmp = path + ".tmp"
    try:
        os.makedirs(DSP_STATE_DIR, exist_ok=True)
        with open(tmp, "w") as f:
            json.dump(undo_stack[-UNDO_DEPTH:], f)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except OSError as e:
        log(f"DSP undo persist failed: {e}")


def load_undo():
    global undo_stack
    try:
        with open(state_path("dsp-undo.json")) as f:
            data = json.load(f)
        if isinstance(data, list):
            undo_stack = data[-UNDO_DEPTH:]
            log(f"DSP undo stack loaded: {len(undo_stack)} entr{'y' if len(undo_stack) == 1 else 'ies'}")
    except FileNotFoundError:
        pass
    except (OSError, ValueError) as e:
        log(f"DSP undo stack unreadable, starting empty: {e}")


def audit(record):
    path = state_path("dsp-apply.log")
    try:
        os.makedirs(DSP_STATE_DIR, exist_ok=True)
        if os.path.exists(path) and os.path.getsize(path) > AUDIT_MAX_BYTES:
            os.replace(path, path + ".1")
        with open(path, "a") as f:
            f.write(json.dumps(record) + "\n")
    except OSError as e:
        log(f"DSP audit write failed: {e}")


def publish_result(result):
    text = f"{result['action']} {result['id']}: {result['status']}"
    if result.get("reason"):
        text += f" - {result['reason']}"
    elif result.get("written") is not None:
        text += f" ({result['written']} written, {result.get('unchanged', 0)} already set)"
    ui_send("/dsp/ui/apply/status", text)
    dsp_forward(build_osc("/dsp/result", json.dumps(result)))
    log(f"DSP {text}")


def publish_pending():
    with pending_lock:
        p = pending_proposal
    if p is None:
        ui_send("/dsp/ui/proposal/summary", "no pending proposal")
        return
    ui_send("/dsp/ui/proposal/summary", f"{p['source']}: {p['summary'] or p['id']} ({len(p['changes'])} controls) - press APPLY to write, UNDO to revert")


def execute_changes(action, pid, source, changes):
    """Snapshot, write the differences, verify read-back. Returns a result dict."""
    result = {"schema_version": RESULT_SCHEMA, "action": action, "id": pid, "source": source,
              "at": datetime.now().isoformat(timespec="seconds")}
    if tcp_sock is None:
        result.update(status="refused", reason="osclive is not connected")
        return result, None
    if action == "apply" and source == "ringout":
        reason = ringout_policy(changes)
        if reason:
            result.update(status="refused", reason=reason)
            return result, None
    reason = settle_check(changes)
    if reason:
        result.update(status="refused", reason=reason)
        return result, None
    snapshot, todo = [], []
    for c in changes:
        cur = current_raw(c["address"], c["kind"])
        if cur is None:
            result.update(status="refused", reason=f"{c['address']}: current value unknown")
            return result, None
        if cur != c["raw"]:
            snapshot.append({"address": c["address"], "raw": cur})
            todo.append(c)
    written = []
    for c in todo:
        if not send_tcp_frame(build_osc(c["address"], raw_to_wire(c["raw"], c["kind"]))):
            result.update(status="failed", reason=f"write to {c['address']} failed; osclive disconnected",
                          written=len(written), unchanged=len(changes) - len(todo))
            return result, [s for s in snapshot if s["address"] in written]
        written.append(c["address"])
        time.sleep(APPLY_WRITE_GAP)
    deadline = time.monotonic() + APPLY_VERIFY_TIMEOUT
    mismatched = todo
    while mismatched and time.monotonic() < deadline:
        time.sleep(0.05)
        mismatched = [c for c in todo if current_raw(c["address"], c["kind"]) != c["raw"]]
    result.update(written=len(written), unchanged=len(changes) - len(todo))
    if mismatched:
        result.update(status="unverified",
                      reason="read-back differs for " + ", ".join(
                          f"{c['address']} (want {c['raw']}, have {current_raw(c['address'], c['kind'])})" for c in mismatched[:4]))
    else:
        result.update(status="verified")
    return result, snapshot


def run_apply(pid):
    global pending_proposal, last_apply_time
    if not apply_lock.acquire(blocking=False):
        publish_result({"schema_version": RESULT_SCHEMA, "action": "apply", "id": str(pid), "status": "refused", "reason": "another apply is in progress"})
        return
    try:
        with pending_lock:
            p = pending_proposal
        if p is None or (pid is not None and pid != p["id"]):
            publish_result({"schema_version": RESULT_SCHEMA, "action": "apply", "id": str(pid), "status": "refused", "reason": "no matching pending proposal"})
            return
        if time.monotonic() - last_apply_time < APPLY_MIN_INTERVAL:
            publish_result({"schema_version": RESULT_SCHEMA, "action": "apply", "id": p["id"], "status": "refused", "reason": "applies are rate-limited; try again"})
            return
        result, snapshot = execute_changes("apply", p["id"], p["source"], p["changes"])
        if result["status"] != "refused":
            last_apply_time = time.monotonic()
            with pending_lock:
                if pending_proposal is p:
                    pending_proposal = None
            if snapshot:
                undo_stack.append({"id": p["id"], "source": p["source"], "at": result["at"], "snapshot": snapshot})
                del undo_stack[:-UNDO_DEPTH]
                persist_undo()
        audit({**result, "summary": p["summary"], "changes": [{"address": c["address"], "raw": c["raw"]} for c in p["changes"]], "before": snapshot})
        publish_result(result)
        publish_pending()
    finally:
        apply_lock.release()


def run_revert():
    global last_apply_time
    if not apply_lock.acquire(blocking=False):
        publish_result({"schema_version": RESULT_SCHEMA, "action": "revert", "id": "", "status": "refused", "reason": "another apply is in progress"})
        return
    try:
        if not undo_stack:
            publish_result({"schema_version": RESULT_SCHEMA, "action": "revert", "id": "", "status": "refused", "reason": "nothing to undo"})
            return
        entry = undo_stack[-1]
        changes = []
        for s in entry["snapshot"]:
            kinfo = control_kind(s["address"])
            if kinfo:
                changes.append({"address": s["address"], "raw": s["raw"], "kind": kinfo[0], "channel": kinfo[1], "name": kinfo[2]})
        result, _ = execute_changes("revert", entry["id"], entry["source"], changes)
        if result["status"] != "refused":
            last_apply_time = time.monotonic()
            if result["status"] == "verified":
                undo_stack.pop()
                persist_undo()
        audit({**result, "restored": entry["snapshot"]})
        publish_result(result)
    finally:
        apply_lock.release()


def submit_proposal(payload):
    global pending_proposal
    raw = osc_first_param(payload)
    try:
        p = json.loads(raw) if isinstance(raw, str) else None
    except ValueError:
        p = None
    proposal, reason = validate_proposal(p)
    pid = p.get("id", "") if isinstance(p, dict) else ""
    if proposal is None:
        publish_result({"schema_version": RESULT_SCHEMA, "action": "submit", "id": str(pid), "status": "rejected", "reason": reason})
        return
    with pending_lock:
        pending_proposal = proposal
    publish_result({"schema_version": RESULT_SCHEMA, "action": "submit", "id": proposal["id"], "status": "pending", "changes": len(proposal["changes"])})
    publish_pending()


def clear_pending():
    global pending_proposal
    with pending_lock:
        pending_proposal = None
    publish_pending()


def start_worker(fn, *args):
    threading.Thread(target=fn, args=args, daemon=True).start()


def handle_dsp(addr, payload, peer):
    if addr == "/dsp/subscribe" or addr == "/dsp/unsubscribe":
        try:
            port = int(float(osc_first_param(payload)))
        except (TypeError, ValueError):
            port = peer[1]
        if not 0 < port < 65536:
            return
        sub = (peer[0], port)
        if addr == "/dsp/unsubscribe":
            with dsp_lock:
                removed = dsp_subscribers.pop(sub, None) is not None
            if removed:
                log(f"DSP subscriber {sub[0]}:{sub[1]} left")
            return
        with dsp_lock:
            new = sub not in dsp_subscribers
            if new and len(dsp_subscribers) >= DSP_MAX_SUBSCRIBERS:
                log(f"DSP subscribe from {sub[0]}:{sub[1]} refused: {DSP_MAX_SUBSCRIBERS} subscribers")
                return
            dsp_subscribers[sub] = time.monotonic() + DSP_SUBSCRIBER_TTL
        if new:
            log(f"DSP subscriber {sub[0]}:{sub[1]} joined")
            dsp_replay(sub)
        return
    if addr.startswith("/dsp/ui/"):
        try:
            udp_out.sendto(payload, (OSC_UI_HOST, OSC_UI_PORT))
        except OSError as e:
            log(f"DSP UI forward error: {e}")
        return
    if addr == "/dsp/proposal/set":
        submit_proposal(payload)
        return
    if addr == "/dsp/proposal/clear":
        clear_pending()
        return
    if addr == "/dsp/apply":
        pid = osc_first_param(payload)
        start_worker(run_apply, pid if isinstance(pid, str) else None)
        return
    if addr == "/dsp/revert":
        start_worker(run_revert)
        return
    log(f"DSP port dropped {addr} from {peer[0]}:{peer[1]}")


def dsp_worker():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind((DSP_LISTEN_HOST, DSP_LISTEN_PORT))
    sock.settimeout(1.0)
    log(f"Listening for DSP clients on {DSP_LISTEN_HOST}:{DSP_LISTEN_PORT}")
    while not stop_event.is_set():
        try:
            payload, peer = sock.recvfrom(65535)
        except socket.timeout:
            continue
        except OSError:
            break
        addr = osc_address(payload)
        if addr:
            handle_dsp(addr, payload, peer)
    sock.close()


def set_tcp(sock):
    global tcp_sock
    with tcp_lock:
        tcp_sock = sock


def clear_tcp(sock):
    global tcp_sock
    with tcp_lock:
        if tcp_sock is sock:
            tcp_sock = None


def send_tcp_frame(payload):
    with tcp_lock:
        sock = tcp_sock
        if sock is None:
            return False
        try:
            sock.sendall(struct.pack(">I", len(payload)) + payload)
            return True
        except OSError as e:
            log(f"TCP send failed: {e}")
            return False


def recv_exact(sock, size):
    buf = bytearray()
    while len(buf) < size and not stop_event.is_set():
        chunk = sock.recv(size - len(buf))
        if not chunk:
            raise ConnectionError("osclive closed connection")
        buf.extend(chunk)
    return bytes(buf)


def tcp_worker():
    while not stop_event.is_set():
        sock = None
        try:
            log(f"Connecting to osclive {OSCLIVE_HOST}:{OSCLIVE_PORT}")
            sock = socket.create_connection((OSCLIVE_HOST, OSCLIVE_PORT), timeout=5)
            sock.settimeout(None)
            set_tcp(sock)
            log("Connected to osclive")

            while not stop_event.is_set():
                hdr = recv_exact(sock, 4)
                length = struct.unpack(">I", hdr)[0]
                if not 0 < length <= MAX_FRAME:
                    raise ConnectionError(f"invalid frame length {length}")
                payload = recv_exact(sock, length)
                cache_packet(payload)
                udp_out.sendto(payload, (OSC_UI_HOST, OSC_UI_PORT))
                dsp_forward(payload)

        except (OSError, ConnectionError) as e:
            if not stop_event.is_set():
                log(f"osclive connection lost: {e}")
        finally:
            if sock is not None:
                clear_tcp(sock)
                try:
                    sock.close()
                except OSError:
                    pass

        if not stop_event.is_set():
            time.sleep(RECONNECT_DELAY)


def send_analyser_selection():
    with proc_lock:
        ch = selected_channel
    try:
        udp_out.sendto(build_osc("/select", ch), ("127.0.0.1", 9100))
    except OSError:
        pass


def analyser_is_running():
    with proc_lock:
        p = analyser_proc
        return bool(p and p.poll() is None)


def analyser_publish():
    with proc_lock:
        p = analyser_proc
        running = int(bool(p and p.poll() is None))
        err = analyser_error
    ui_send("/analyser/running", running)
    ui_send("/analyser/error", err)


def analyser_output_worker(proc):
    global analyser_proc, analyser_error, analyser_stop_requested
    try:
        if proc.stdout:
            for raw in iter(proc.stdout.readline, b""):
                if not raw:
                    break
                line = raw.decode(errors="replace").rstrip()
                if line:
                    log(f"[RTA] {line}")
    finally:
        rc = proc.poll()
        if rc is None:
            try:
                rc = proc.wait(timeout=0.5)
            except Exception:
                rc = None
        with proc_lock:
            requested = analyser_stop_requested
            if analyser_proc is proc:
                analyser_proc = None
                if rc not in (None, 0) and not requested:
                    analyser_error = f"RTA exited with code {rc}"
            analyser_stop_requested = False
        analyser_publish()


def analyser_start():
    global analyser_proc, analyser_error, analyser_stop_requested

    with proc_lock:
        if analyser_proc and analyser_proc.poll() is None:
            analyser_error = ""
            already = True
        else:
            already = False

    if already:
        analyser_publish()
        send_analyser_selection()
        return

    script = os.path.expanduser(ANALYSER_SCRIPT)
    if not os.path.isfile(script):
        with proc_lock:
            analyser_error = f"RTA script not found: {script}"
        analyser_publish()
        return

    try:
        proc = subprocess.Popen(
            [sys.executable, "-u", script],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL,
            bufsize=0,
        )
    except Exception as e:
        with proc_lock:
            analyser_error = str(e)
        analyser_publish()
        return

    with proc_lock:
        analyser_proc = proc
        analyser_error = ""
        analyser_stop_requested = False

    log(f"Started RTA PID {proc.pid}")
    threading.Thread(target=analyser_output_worker, args=(proc,), daemon=True).start()
    time.sleep(0.35)
    send_analyser_selection()
    analyser_publish()


def analyser_stop():
    global analyser_proc, analyser_error, analyser_stop_requested
    with proc_lock:
        proc = analyser_proc
        if proc and proc.poll() is None:
            analyser_stop_requested = True

    if proc and proc.poll() is None:
        log(f"Stopping RTA PID {proc.pid}")
        try:
            proc.terminate()
            proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=1)
        except Exception as e:
            with proc_lock:
                analyser_error = str(e)

    with proc_lock:
        if analyser_proc is proc:
            analyser_proc = None
        analyser_error = ""
        analyser_stop_requested = False
    analyser_publish()


def analyser_toggle():
    if analyser_is_running():
        analyser_stop()
    else:
        analyser_start()


def disk_free_gb():
    try:
        os.makedirs(RECORD_DIR, exist_ok=True)
        st = os.statvfs(RECORD_DIR)
        return (st.f_bavail * st.f_frsize) / (1024**3)
    except Exception:
        return 0.0


def recorder_is_running():
    with proc_lock:
        p = recorder_proc
        return bool(p and p.poll() is None)


def recording_publish():
    with proc_lock:
        p = recorder_proc
        running = int(bool(p and p.poll() is None))
        err = recording_error
        path = recording_file
        started = recording_started
    elapsed = int(max(0, time.monotonic() - started)) if running and started else 0
    ui_send("/recording/running", running)
    ui_send("/recording/elapsed", elapsed)
    ui_send("/recording/disk_free_gb", disk_free_gb())
    ui_send("/recording/file", os.path.basename(path) if path else "")
    ui_send("/recording/error", err)


def recorder_output_worker(proc):
    global recorder_proc, recording_error, recording_stop_requested
    last_lines = []
    try:
        if proc.stdout:
            for raw in iter(proc.stdout.readline, b""):
                if not raw:
                    break
                line = raw.decode(errors="replace").rstrip()
                if line:
                    log(f"[REC] {line}")
                    last_lines.append(line)
                    last_lines = last_lines[-4:]
    finally:
        rc = proc.poll()
        if rc is None:
            try:
                rc = proc.wait(timeout=0.5)
            except Exception:
                rc = None

        with proc_lock:
            requested = recording_stop_requested
            if recorder_proc is proc:
                recorder_proc = None
                if rc not in (None, 0) and not requested:
                    recording_error = last_lines[-1] if last_lines else f"Recorder exited with code {rc}"
            recording_stop_requested = False
        recording_publish()


def recording_start():
    global recorder_proc, recording_error, recording_file
    global recording_started, recording_stop_requested

    with proc_lock:
        if recorder_proc and recorder_proc.poll() is None:
            already = True
        else:
            already = False

    if already:
        recording_publish()
        return

    script = os.path.expanduser(RECORDER_SCRIPT)
    if not os.path.isfile(script):
        with proc_lock:
            recording_error = f"Recorder script not found: {script}"
        recording_publish()
        return

    os.makedirs(RECORD_DIR, exist_ok=True)
    filename = datetime.now().strftime("StudioLive_%Y-%m-%d_%H-%M-%S.rf64.wav")
    path = os.path.join(RECORD_DIR, filename)

    try:
        proc = subprocess.Popen(
            [sys.executable, "-u", script, path],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL,
            bufsize=0,
        )
    except Exception as e:
        with proc_lock:
            recording_error = str(e)
        recording_publish()
        return

    with proc_lock:
        recorder_proc = proc
        recording_error = ""
        recording_file = path
        recording_started = time.monotonic()
        recording_stop_requested = False

    log(f"Started recorder PID {proc.pid}: {path}")
    threading.Thread(target=recorder_output_worker, args=(proc,), daemon=True).start()
    time.sleep(0.35)
    recording_publish()


def recording_stop():
    global recorder_proc, recording_stop_requested
    with proc_lock:
        proc = recorder_proc
        if proc and proc.poll() is None:
            recording_stop_requested = True

    if proc and proc.poll() is None:
        log(f"Stopping recorder PID {proc.pid}")
        try:
            proc.terminate()
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=2)
        except Exception as e:
            with proc_lock:
                global recording_error
                recording_error = str(e)

    with proc_lock:
        if recorder_proc is proc:
            recorder_proc = None
    recording_publish()


def status_worker():
    while not stop_event.wait(1.0):
        analyser_publish()
        recording_publish()


def handle_local(addr, payload):
    global selected_channel

    if addr == "/bridge/resync":
        replay_cache()
        return True

    if addr in ("/dsp/apply", "/dsp/revert"):
        try:
            pressed = float(osc_first_param(payload)) >= 0.5
        except (TypeError, ValueError):
            pressed = False
        if pressed:
            start_worker(run_apply, None) if addr == "/dsp/apply" else start_worker(run_revert)
        return True

    if addr.startswith("/dsp/"):
        return True

    if addr == "/analyser/start":
        analyser_start()
        return True
    if addr == "/analyser/stop":
        analyser_stop()
        return True
    if addr == "/analyser/toggle":
        analyser_toggle()
        return True
    if addr == "/analyser/status":
        analyser_publish()
        return True
    if addr == "/analyser/select":
        try:
            ch = int(float(osc_first_param(payload)))
            if 1 <= ch <= 12:
                with proc_lock:
                    selected_channel = ch
                send_analyser_selection()
        except Exception:
            pass
        return True

    if addr == "/recording/start":
        recording_start()
        return True
    if addr == "/recording/stop":
        recording_stop()
        return True
    if addr == "/recording/status":
        recording_publish()
        return True

    return False


def udp_worker():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind((UDP_LISTEN_HOST, UDP_LISTEN_PORT))
    sock.settimeout(1.0)
    log(f"Listening for Open Stage Control UDP {UDP_LISTEN_PORT}")

    while not stop_event.is_set():
        try:
            payload, peer = sock.recvfrom(65535)
        except socket.timeout:
            continue
        except OSError:
            break

        addr = osc_address(payload)

        if addr and handle_local(addr, payload):
            continue

        if not send_tcp_frame(payload):
            log(f"Dropped {addr or 'OSC packet'} from {peer}: osclive not connected")

    sock.close()


def shutdown(signum=None, frame=None):
    if stop_event.is_set():
        return
    log("Stopping bridge")
    stop_event.set()
    try:
        recording_stop()
    except Exception:
        pass
    try:
        analyser_stop()
    except Exception:
        pass
    with tcp_lock:
        sock = tcp_sock
        if sock:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


signal.signal(signal.SIGINT, shutdown)
signal.signal(signal.SIGTERM, shutdown)

load_undo()

threads = [
    threading.Thread(target=tcp_worker, daemon=True),
    threading.Thread(target=udp_worker, daemon=True),
    threading.Thread(target=status_worker, daemon=True),
    threading.Thread(target=dsp_worker, daemon=True),
]
for t in threads:
    t.start()

analyser_publish()
recording_publish()

try:
    while not stop_event.is_set():
        time.sleep(0.5)
finally:
    shutdown()
    for t in threads:
        t.join(timeout=2)
    udp_out.close()
    log("Bridge stopped")
