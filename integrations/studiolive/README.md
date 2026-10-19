# StudioLive integration files

`deployed/` is a byte-for-byte baseline of what runs on `mixer@studiolive`, captured on 2026-10-19. Commit changes against it so bridge and Open Stage Control diffs stay reviewable. It is not deployed automatically.

- `osc_tcp1_bridge_jack.py` — UDP 9000/9001 ↔ osclive OSC/TCP 4301 bridge, state cache, RTA and recorder lifecycle
- `studiolive_spectrum_jack.py` — JACK 31-band RTA client
- `studiolive_jack_recorder.py` — JACK 16-channel RF64 recorder
- `StudioLive1602_OpenStageControl_v26.json` — Open Stage Control session (MIX, EQ, DYNAMICS, METERS)
- `systemd/` — `studiolive-{jack,osclive,bridge,osc}.service`
- `studiolive-blacklist.conf` — `/etc/modprobe.d` entry blacklisting `snd_dice`

`.reference/` (gitignored) holds a local copy of osclive 0.3.0 for reading only. It is MIT-licensed upstream at [martinspinler/osclive](https://github.com/martinspinler/osclive), commit `1da2ada424f5c16afc64b173bc5dbefcaffccd63`. Refresh it with:

```bash
mkdir -p integrations/studiolive/.reference
scp -r mixer@studiolive:/home/mixer/.venvs/osclive/lib/python3.12/site-packages/osclive integrations/studiolive/.reference/
```

See [`docs/StudioLive-Integration.md`](../../docs/StudioLive-Integration.md) for the protocol facts and phase plan.
