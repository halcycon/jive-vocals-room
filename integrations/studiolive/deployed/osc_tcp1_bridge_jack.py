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
"""

import os
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
UDP_LISTEN_PORT = 9000
OSC_UI_HOST = "127.0.0.1"
OSC_UI_PORT = 9001

OSCLIVE_HOST = "127.0.0.1"
OSCLIVE_PORT = 4301
RECONNECT_DELAY = 2.0
MAX_FRAME = 1024 * 1024

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


def cache_packet(data):
    try:
        packet = OscPacket(data)
        for timed in packet.messages:
            msg = timed.message
            if msg.address and msg.dgram:
                with cache_lock:
                    state_cache[msg.address] = msg.dgram
                    state_cache.move_to_end(msg.address)
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

threads = [
    threading.Thread(target=tcp_worker, daemon=True),
    threading.Thread(target=udp_worker, daemon=True),
    threading.Thread(target=status_worker, daemon=True),
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
