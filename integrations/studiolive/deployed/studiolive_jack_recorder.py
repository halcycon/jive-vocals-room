#!/usr/bin/env python3
"""
StudioLive 16.0.2 JACK multitrack recorder.

Records all 16 FireWire input channels to one 16-channel RF64 file.
RF64 is used deliberately so long recordings are not limited by WAV's 4 GiB size.

Requires:
  jack-client
  numpy
  soundfile
"""

import argparse
import os
import queue
import re
import signal
import threading
import time

import jack
import numpy as np
import soundfile as sf

stop_event = threading.Event()
audio_q = queue.Queue(maxsize=256)
dropped_blocks = 0

def find_sources(client):
    ports = client.get_ports(is_output=True, is_physical=True)
    found = []
    for ch in range(1, 17):
        pat = re.compile(rf"^firewire_pcm:.*_Ch {ch}_in$")
        match = next((p for p in ports if pat.match(p.name)), None)
        if match is None:
            raise RuntimeError(f"Could not find StudioLive FireWire source CH {ch}")
        found.append(match)
    return found

def main():
    global dropped_blocks

    ap = argparse.ArgumentParser()
    ap.add_argument("filename")
    args = ap.parse_args()

    filename = os.path.abspath(os.path.expanduser(args.filename))
    os.makedirs(os.path.dirname(filename), exist_ok=True)

    client = jack.Client("studiolive_recorder", no_start_server=True)
    inputs = [client.inports.register(f"ch{ch:02d}") for ch in range(1, 17)]
    sources = find_sources(client)

    @client.set_process_callback
    def process(frames):
        global dropped_blocks
        block = np.empty((frames, 16), dtype=np.float32)
        for i, port in enumerate(inputs):
            block[:, i] = port.get_array()[:frames]
        try:
            audio_q.put_nowait(block)
        except queue.Full:
            dropped_blocks += 1

    recorder = sf.SoundFile(
        filename,
        mode="w",
        samplerate=client.samplerate,
        channels=16,
        subtype="PCM_24",
        format="RF64",
    )

    def writer():
        while not stop_event.is_set() or not audio_q.empty():
            try:
                block = audio_q.get(timeout=0.25)
            except queue.Empty:
                continue
            recorder.write(block)

    writer_thread = threading.Thread(target=writer, name="disk-writer", daemon=True)

    def request_stop(signum=None, frame=None):
        stop_event.set()

    signal.signal(signal.SIGINT, request_stop)
    signal.signal(signal.SIGTERM, request_stop)

    client.activate()
    for src, dst in zip(sources, inputs):
        client.connect(src, dst)

    writer_thread.start()

    print(f"RECORDING_FILE={filename}", flush=True)
    print(f"SAMPLE_RATE={client.samplerate}", flush=True)
    print("CHANNELS=16", flush=True)

    try:
        while not stop_event.is_set():
            time.sleep(0.2)
    finally:
        stop_event.set()
        writer_thread.join(timeout=15)
        recorder.flush()
        recorder.close()
        try:
            client.deactivate()
        except Exception:
            pass
        client.close()
        print(f"DROPPED_BLOCKS={dropped_blocks}", flush=True)

if __name__ == "__main__":
    main()
