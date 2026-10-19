#!/usr/bin/env python3
"""
StudioLive 16.0.2 JACK RTA
- One JACK input port.
- Dynamically reconnects to selected StudioLive input CH1..CH12.
- Receives OSC /select N on UDP 9100.
- Sends 31-band dBFS values to Open Stage Control UDP 9001.
"""

import argparse
import socket
import threading
import time
import re
import queue
import numpy as np
import jack
from pythonosc.osc_packet import OscPacket
from pythonosc.osc_message_builder import OscMessageBuilder

BANDS = np.array(
    [20,25,32,40,50,63,80,100,125,160,200,250,320,400,500,640,800,
     1000,1300,1600,2000,2500,3200,4000,5000,6400,8000,10000,13000,16000,20000],
    dtype=float
)

selected = 1
selected_lock = threading.Lock()
audio_q = queue.Queue(maxsize=24)
stop_event = threading.Event()

def osc_send(sock, host, port, addr, value):
    b = OscMessageBuilder(address=addr)
    b.add_arg(float(value))
    sock.sendto(b.build().dgram, (host, port))

def control_worker(port):
    global selected
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("127.0.0.1", port))
    s.settimeout(1)
    while not stop_event.is_set():
        try:
            data, _ = s.recvfrom(4096)
        except socket.timeout:
            continue
        try:
            packet = OscPacket(data)
            for timed in packet.messages:
                m = timed.message
                if m.address == "/select" and m.params:
                    n = int(float(m.params[0]))
                    if 1 <= n <= 12:
                        with selected_lock:
                            selected = n
        except Exception:
            pass
    s.close()

def find_firewire_source(client, ch):
    pat = re.compile(rf"^firewire_pcm:.*_Ch {ch}_in$")
    for p in client.get_ports(is_output=True, is_physical=True):
        if pat.match(p.name):
            return p
    return None

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--control-port", type=int, default=9100)
    ap.add_argument("--osc-host", default="127.0.0.1")
    ap.add_argument("--osc-port", type=int, default=9001)
    ap.add_argument("--fft", type=int, default=4096)
    args = ap.parse_args()

    out = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    client = jack.Client("studiolive_rta", no_start_server=True)
    inport = client.inports.register("input")

    current_source = None
    current_ch = None
    window = np.hanning(args.fft).astype(np.float32)
    freqs = np.fft.rfftfreq(args.fft, 1.0 / client.samplerate)

    edges = np.empty(len(BANDS)+1)
    edges[1:-1] = np.sqrt(BANDS[:-1] * BANDS[1:])
    edges[0] = max(10.0, BANDS[0]**2 / edges[1])
    edges[-1] = min(client.samplerate/2, BANDS[-1]**2 / edges[-2])

    smooth = np.full(len(BANDS), -72.0, dtype=float)

    @client.set_process_callback
    def process(frames):
        try:
            block = np.array(inport.get_array()[:frames], copy=True)
            audio_q.put_nowait(block)
        except queue.Full:
            pass

    client.activate()
    threading.Thread(target=control_worker, args=(args.control_port,), daemon=True).start()

    acc = np.empty(0, dtype=np.float32)

    try:
        while True:
            with selected_lock:
                ch = selected

            if ch != current_ch:
                try:
                    if current_source:
                        client.disconnect(current_source, inport)
                except jack.JackError:
                    pass
                src = find_firewire_source(client, ch)
                if src:
                    client.connect(src, inport)
                    current_source = src
                    current_ch = ch
                    print(f"RTA connected to {src.name}", flush=True)
                else:
                    current_source = None
                    current_ch = ch
                    print(f"RTA source for CH{ch} not found", flush=True)

            try:
                block = audio_q.get(timeout=0.5)
            except queue.Empty:
                continue

            acc = np.concatenate((acc, block.astype(np.float32, copy=False)))
            while len(acc) >= args.fft:
                x = acc[:args.fft]
                acc = acc[args.fft:]

                spec = np.abs(np.fft.rfft(x * window))
                spec = spec / (np.sum(window)/2.0 + 1e-12)

                db = np.full(len(BANDS), -72.0, dtype=float)
                for i in range(len(BANDS)):
                    mask = (freqs >= edges[i]) & (freqs < edges[i+1])
                    if np.any(mask):
                        rms = np.sqrt(np.mean(spec[mask]**2))
                        db[i] = max(-72.0, min(0.0, 20*np.log10(rms + 1e-12)))

                smooth = np.where(db > smooth, db, smooth*0.78 + db*0.22)

                for i, v in enumerate(smooth, 1):
                    osc_send(out, args.osc_host, args.osc_port, f"/analyser/band/{i}", v)

    except KeyboardInterrupt:
        pass
    finally:
        stop_event.set()
        try:
            client.deactivate()
        except Exception:
            pass
        client.close()
        out.close()

if __name__ == "__main__":
    main()
