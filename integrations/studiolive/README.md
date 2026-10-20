# StudioLive integration files

`deployed/` is a byte-for-byte baseline of what runs on `mixer@studiolive`, captured on 2026-10-19. Commit changes against it so bridge and Open Stage Control diffs stay reviewable. It is not deployed automatically.

- `osc_tcp1_bridge_jack.py` — UDP 9000/9001 ↔ osclive OSC/TCP 4301 bridge, state cache, RTA and recorder lifecycle
- `studiolive_spectrum_jack.py` — JACK 31-band RTA client
- `studiolive_jack_recorder.py` — JACK 16-channel RF64 recorder
- `StudioLive1602_OpenStageControl_v26.json` — Open Stage Control session (MIX, EQ, DYNAMICS, METERS)
- `systemd/` — `studiolive-{jack,osclive,bridge,osc}.service`
- `studiolive-blacklist.conf` — `/etc/modprobe.d` entry blacklisting `snd_dice`

`bridge/` holds the maintained bridge. It is the deployed baseline plus the read-only `/dsp` state port (`127.0.0.1:9002`). Install it with `just deploy-studiolive-bridge`, which restarts the bridge and, through systemd, Open Stage Control. Compare it with `git diff --no-index deployed/osc_tcp1_bridge_jack.py bridge/osc_tcp1_bridge_jack.py`. The mini keeps timestamped `.bak-*` copies, plus `osc_tcp1_bridge_jack.py.orig-20261019` (the pre-change original).

`bridge/fake_osclive.py` is a hardware-free osclive stand-in: it speaks OSC/TCP 1.0, echoes writes, then sends a truncated desk confirmation, and can simulate knob moves. `bridge/test_bridge_dsp.py` runs the real bridge against it on spare ports (`just test-bridge python=/path/with/python-osc`, or `/home/mixer/.venvs/osclive/bin/python` on the mini). Set `JIVE_ROOM_BIN` to include the CLI profile test. The bridge reads `STUDIOLIVE_OSCLIVE_PORT`, `STUDIOLIVE_UI_LISTEN_PORT`, `STUDIOLIVE_UI_PORT`, `STUDIOLIVE_DSP_PORT`, and `STUDIOLIVE_DSP_STATE_DIR`, which default to the production values.

`osc/` holds `add_room_tab.py`, which adds a display-only ROOM tab to the v26 baseline session, and its generated output, `StudioLive1602_OpenStageControl_v27.json`. `just deploy-studiolive-osc` regenerates the session and installs it. Because the unit still loads `..._v26.json`, the mini has `..._v26.json -> ..._v27.json` as a symlink, with the original at `..._v26.json.orig`. The v26 baseline already repeats some widget IDs (`select_ch1`…), and the ROOM tab adds none.

`sudoers/studiolive-dsp` is the narrow passwordless-restart rule. The installed version (2026-10-19) covers the bridge and Open Stage Control. The repository version also allows `systemctl restart studiolive-room.service`; reinstall it to enable that.

`~/studiolive/dsp-config.json` on the mini is read by the bridge, and is absent by default. `{"allow_auto_cuts": true}` is the first of three locks for automatic cuts; the operator's ENABLE in the ROOM tab and a measured calibration are the others.

`systemd/studiolive-room.service` runs the `jive-room` daemon for the ROOM tab. It uses `Wants=`/`After=` the bridge, so neither can stop the other. PA measurement stays disabled until `-play-pink-to` is added to `ExecStart`. Install it on the mini:

```bash
scp integrations/studiolive/systemd/studiolive-room.service integrations/studiolive/sudoers/studiolive-dsp mixer@studiolive:
ssh -t mixer@studiolive '
  pkill -x jive-room   # stop the temporary setsid daemon
  sudo install -m 0644 studiolive-room.service /etc/systemd/system/ &&
  sudo install -m 0440 -o root -g root studiolive-dsp /etc/sudoers.d/studiolive-dsp &&
  sudo visudo -cf /etc/sudoers.d/studiolive-dsp &&
  sudo systemctl daemon-reload && sudo systemctl enable --now studiolive-room.service'
```

`systemd/studiolive-jack.service` is the maintained JACK unit: `jackd -d firewire -r 48000 -p 128 -n 4` (the baseline ran 44.1 kHz, 1024 × 3). FireWire playback to the desk is proven at 48 kHz.

`fwsink/` makes the mini's desktop audio play into FireWire returns 15/16 (desk ch12, FireWire input on). `fw-sink.conf` is a standalone PipeWire client running the JACK-tunnel module as the sink "StudioLive FW 15/16". `studiolive-fw-sink.py` supervises it, patches its two ports to returns 15/16, and restarts it after a JACK restart. `studiolive-fw-sink.service` is a systemd user unit. Install as `mixer`:

```bash
cd integrations/studiolive/fwsink
ssh mixer@studiolive 'mkdir -p ~/.local/bin ~/.config/studiolive ~/.config/systemd/user'
scp studiolive-fw-sink.py mixer@studiolive:.local/bin/studiolive-fw-sink
scp fw-sink.conf mixer@studiolive:.config/studiolive/fw-sink.conf
scp studiolive-fw-sink.service mixer@studiolive:.config/systemd/user/
ssh mixer@studiolive 'chmod +x ~/.local/bin/studiolive-fw-sink &&
  systemctl --user daemon-reload && systemctl --user enable --now studiolive-fw-sink &&
  pactl set-default-sink studiolive_fw_1516'
```

`.reference/` (gitignored) holds a local copy of osclive 0.3.0 for reading only. It is MIT-licensed upstream at [martinspinler/osclive](https://github.com/martinspinler/osclive), commit `1da2ada424f5c16afc64b173bc5dbefcaffccd63`. Refresh it with:

```bash
mkdir -p integrations/studiolive/.reference
scp -r mixer@studiolive:/home/mixer/.venvs/osclive/lib/python3.12/site-packages/osclive integrations/studiolive/.reference/
```

See [`docs/StudioLive-Integration.md`](../../docs/StudioLive-Integration.md) for the protocol facts and phase plan.
