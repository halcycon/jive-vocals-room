#!/home/mixer/.venvs/osclive/bin/python
"""Keep the "StudioLive FW 1 (mono)" desktop sink alive and patched.

Runs the PipeWire JACK-tunnel client from fw-sink.conf and connects its JACK
port to the desk's FireWire return 1. The tunnel does not survive a
JACK restart, so whenever JACK goes away or the tunnel ports stay missing the
PipeWire client is restarted. Only this connection is ever made; no
other JACK routing is touched.
"""

import os
import signal
import subprocess
import sys
import threading
import time

import jack

CONF = os.path.expanduser("~/.config/studiolive/fw-sink.conf")
CLIENT = "pipewire-fw"
PATCH = {"playback_MONO": "_Ch 1_out"}
POLL_SECONDS = 2
TUNNEL_START_GRACE_SECONDS = 10


def log(msg):
    print(msg, flush=True)


class Tunnel:
    def __init__(self):
        self.proc = None
        self.started = 0.0

    def ensure(self):
        if self.proc is None or self.proc.poll() is not None:
            self.proc = subprocess.Popen(["pipewire", "-c", CONF])
            self.started = time.monotonic()
            log(f"started tunnel pid {self.proc.pid}")

    def stop(self):
        if self.proc is not None and self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
            log("stopped tunnel")
        self.proc = None

    def overdue(self):
        return time.monotonic() - self.started > TUNNEL_START_GRACE_SECONDS


def return_port(client, suffix):
    for p in client.get_ports(is_input=True, is_physical=True, is_audio=True):
        if p.name.endswith(suffix):
            return p.name
    return None


def patch(client):
    """Connects missing patch cables; returns False while tunnel ports are absent."""
    for src_short, dst_suffix in PATCH.items():
        src = f"{CLIENT}:{src_short}"
        try:
            port = client.get_port_by_name(src)
        except jack.JackError:
            return False
        dst = return_port(client, dst_suffix)
        if dst is None:
            return False
        if dst not in [p.name for p in client.get_all_connections(port)]:
            client.connect(src, dst)
            log(f"connected {src} -> {dst}")
    return True


def run(tunnel, stop):
    try:
        client = jack.Client("studiolive-fw-patch", no_start_server=True)
    except jack.JackError:
        return
    gone = threading.Event()
    client.set_shutdown_callback(lambda status, reason: gone.set())
    client.activate()
    log("connected to JACK")
    try:
        while not stop.is_set() and not gone.is_set():
            tunnel.ensure()
            if not patch(client) and tunnel.overdue():
                log("tunnel ports missing; restarting tunnel")
                tunnel.stop()
            stop.wait(POLL_SECONDS)
    finally:
        if not gone.is_set():
            client.deactivate()
            client.close()
        log("JACK went away" if gone.is_set() else "stopping")
        tunnel.stop()


def main():
    stop = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stop.set())
    tunnel = Tunnel()
    while not stop.is_set():
        run(tunnel, stop)
        stop.wait(POLL_SECONDS)
    tunnel.stop()
    return 0


if __name__ == "__main__":
    sys.exit(main())
