#!/usr/bin/env python3
"""Fake osclive for bridge tests. Never talks to hardware.

Mirrors the osclive 0.3.0 behaviour the bridge depends on:
- OSC/TCP 1.0 with 4-byte big-endian length prefixes on a local port;
- on connect, sends every control (as SLClientHandler.init does);
- a write is clamped, stored as the float received, and echoed to every
  client immediately (SLRemote.set_control);
- about 150 ms later the "desk" reports the stored raw value int(v*255)/255,
  which is sent only if it differs from the float (SLBackend._update_control);
- desk_change() simulates a physical knob move.

Usage as a script: fake_osclive.py [port]   (default 14301)
"""

import socket
import struct
import sys
import threading
import time

from pythonosc.osc_message_builder import OscMessageBuilder
from pythonosc.osc_packet import OscPacket

INPUT_CONTROLS = {
    "gain": 0.769, "pan": 0.5, "linkedpan": 0.5, "aux1": 0.0, "aux2": 0.0, "aux3": 0.0, "aux4": 0.0,
    "fxa": 0.0, "fxb": 0.0, "hpf": 0, "hpffreq": 96 / 255, "gate": 0, "gatethresh": 0.0,
    "phantom": 0, "firewire": 0, "phase": 0, "post": 0, "mute": 0, "solo": 0,
    "comp": 0, "compauto": 0, "complimit": 0, "compthresh": 0.5, "compratio": 0.0,
    "compresponse": 0.0, "compgain": 0.0, "eqlow": 0, "eqmid": 0, "eqhigh": 0,
    "eqlowshelf": 0, "eqmidhiq": 0, "eqhighshelf": 0, "eqlowfreq": 128 / 255,
    "eqmidfreq": 128 / 255, "eqhighfreq": 128 / 255, "eqlowgain": 128 / 255,
    "eqmidgain": 128 / 255, "eqhighgain": 128 / 255, "toMain": 1,
}
SWITCHES = {k for k, v in INPUT_CONTROLS.items() if isinstance(v, int)}
DESK_DELAY = 0.15


def osc(addr, value):
    b = OscMessageBuilder(address=addr)
    b.add_arg(value)
    return b.build().dgram


class FakeOsclive:
    def __init__(self, port=14301):
        self.port = port
        self.state = {}
        for n in range(1, 13):
            for ctrl, v in INPUT_CONTROLS.items():
                self.state[f"/channel/ch{n}/{ctrl}"] = v
        for i in range(1, 32):
            self.state[f"/channel/geq0/{i}"] = 127 / 255
        self.state["/channel/geq0/enable"] = 0
        self.writes = []  # (monotonic, address, value)
        self.lock = threading.Lock()
        self.clients = []
        self.stop_event = threading.Event()
        self.srv = socket.create_server(("127.0.0.1", port))
        self.srv.settimeout(0.2)
        threading.Thread(target=self._accept, daemon=True).start()

    def close(self):
        self.stop_event.set()
        self.srv.close()
        with self.lock:
            for c in self.clients:
                try:
                    c.close()
                except OSError:
                    pass

    def _send_all(self, payload):
        frame = struct.pack(">I", len(payload)) + payload
        with self.lock:
            clients = list(self.clients)
        for c in clients:
            try:
                c.sendall(frame)
            except OSError:
                pass

    def _accept(self):
        while not self.stop_event.is_set():
            try:
                conn, _ = self.srv.accept()
            except (socket.timeout, OSError):
                continue
            with self.lock:
                self.clients.append(conn)
                snapshot = list(self.state.items())
            for addr, v in snapshot:
                payload = osc(addr, v)
                try:
                    conn.sendall(struct.pack(">I", len(payload)) + payload)
                except OSError:
                    break
            threading.Thread(target=self._serve, args=(conn,), daemon=True).start()

    def _serve(self, conn):
        try:
            while not self.stop_event.is_set():
                hdr = self._recv(conn, 4)
                if hdr is None:
                    return
                n = struct.unpack(">I", hdr)[0]
                payload = self._recv(conn, n)
                if payload is None:
                    return
                for timed in OscPacket(payload).messages:
                    m = timed.message
                    if m.params:
                        self._write(m.address, m.params[0])
        finally:
            with self.lock:
                if conn in self.clients:
                    self.clients.remove(conn)
            try:
                conn.close()
            except OSError:
                pass

    @staticmethod
    def _recv(conn, n):
        buf = b""
        while len(buf) < n:
            try:
                chunk = conn.recv(n - len(buf))
            except OSError:
                return None
            if not chunk:
                return None
            buf += chunk
        return buf

    def _write(self, addr, value):
        with self.lock:
            if addr not in self.state:
                return
            self.writes.append((time.monotonic(), addr, value))
            v = max(0.0, min(1.0, float(value)))
            self.state[addr] = v
        self._send_all(osc(addr, v))
        threading.Timer(DESK_DELAY, self._desk_confirm, args=(addr,)).start()

    def _desk_confirm(self, addr):
        name = addr.rsplit("/", 1)[1]
        with self.lock:
            v = self.state[addr]
            if name in SWITCHES and addr.startswith("/channel/ch"):
                desk = 1 if v >= 0.5 else 0
            else:
                desk = int(v * 255) / 255
            if desk == v:
                return
            self.state[addr] = desk
        self._send_all(osc(addr, desk))

    def desk_change(self, addr, value):
        """Simulate a physical control move on the desk."""
        with self.lock:
            self.state[addr] = value
        self._send_all(osc(addr, value))

    def raw(self, addr):
        """The stored value as osclive would hold it: 0/1 for switches, else 0-255."""
        with self.lock:
            v = max(0.0, min(1.0, float(self.state[addr])))
        if addr.rsplit("/", 1)[1] in SWITCHES and addr.startswith("/channel/ch"):
            return 1 if v >= 0.5 else 0
        return int(v * 255 + 1e-6)


if __name__ == "__main__":
    f = FakeOsclive(int(sys.argv[1]) if len(sys.argv) > 1 else 14301)
    print(f"fake osclive on 127.0.0.1:{f.port}", flush=True)
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        f.close()
