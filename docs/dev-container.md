# Development container (Podman)

Live PipeWire headers stay in a toolchain image so the host does not need
`libpipewire` development packages. This follows the same pattern as
thingino-firmware's `build-container.sh`: Podman first, Docker fallback,
`--userns=keep-id`, and shared SELinux `:z` labels on Enforcing hosts.

The image is Fedora, not Debian. `ffmpeg-statigo`'s prebuilt `libffmpeg.a`
needs a current glibc (`__isoc23_*`); Bookworm cannot link it. Fedora's
PipeWire pkg-config also injects `-fno-strict-overflow`; `just` exports
`CGO_CFLAGS_ALLOW` so cgo will accept it.

```bash
just container              # interactive shell in a one-shot container
just container test         # just test + C ring harness
just container test-live    # also -tags pipewire
just container build-live   # jive-room with PipeWire capture
just container live         # tagged -live, host PipeWire socket bind-mounted
just container distrobox    # persistent distrobox from the same image
just container rebuild-image
```

The image is `localhost/jive-room-dev:latest`, built from `Containerfile`.
The repository is bind-mounted at `/workspace`. Go caches live in
`.gocache/` and `.gomodcache/` (gitignored).

Default `just test` / `just build-room` stay hermetic and do not link
PipeWire. `just build-room-live` and `just test-pipewire` require the
headers from this image (or an equivalent host/distrobox install).

`just container live` bind-mounts only the host `pipewire-0` socket into
`/tmp` inside the container. Tagged hardware tests still require
`JIVE_LIVE_PW=1` and are skipped in CI. Distrobox is the better interactive
path for a full desktop session.

How to use the analyser and the offline sound-check is in [Room-Guide.md](Room-Guide.md).
