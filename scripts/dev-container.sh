#!/usr/bin/env bash
#
# Podman/Docker toolchain wrapper, modelled on thingino-firmware's build-container.sh.
#
# Usage:
#   ./scripts/dev-container.sh              # interactive shell
#   ./scripts/dev-container.sh shell        # same
#   ./scripts/dev-container.sh test         # hermetic Go tests + C ring harness
#   ./scripts/dev-container.sh test-live    # also build/test -tags pipewire
#   ./scripts/dev-container.sh build        # jive-vocals + jive-room
#   ./scripts/dev-container.sh build-live   # jive-room with PipeWire capture
#   ./scripts/dev-container.sh live         # build tagged binary and run -live (binds host PipeWire socket)
#   ./scripts/dev-container.sh distrobox    # create/enter a persistent distrobox
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

IMAGE_NAME="${JIVE_DEV_IMAGE:-localhost/jive-room-dev:latest}"
CONTAINER_NAME="${JIVE_DEV_CONTAINER:-jive-room-dev}"
DISTROBOX_NAME="${JIVE_DISTROBOX_NAME:-jive-room-dev}"

if command -v podman >/dev/null 2>&1; then
	ENGINE=podman
elif command -v docker >/dev/null 2>&1; then
	ENGINE=docker
else
	echo "Neither podman nor docker found. Install podman (preferred) or docker." >&2
	exit 1
fi

volume_suffix=""
volume_desc="none"
if [ -n "${CONTAINER_VOLUME_LABEL:-}" ]; then
	case "$CONTAINER_VOLUME_LABEL" in
	none | off | "") volume_suffix="" ; volume_desc="none (forced off)" ;;
	*) volume_suffix=":$CONTAINER_VOLUME_LABEL" ; volume_desc="$CONTAINER_VOLUME_LABEL" ;;
	esac
elif command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
	volume_suffix=":z"
	volume_desc="z (SELinux Enforcing)"
fi

build_image() {
	echo "Building $IMAGE_NAME with $ENGINE (volume label: $volume_desc)"
	$ENGINE build -t "$IMAGE_NAME" -f "$ROOT_DIR/Containerfile" "$ROOT_DIR"
}

image_exists() {
	if [ "$ENGINE" = "podman" ]; then
		podman image exists "$IMAGE_NAME"
	else
		docker image inspect "$IMAGE_NAME" >/dev/null 2>&1
	fi
}

ensure_image() {
	if [ "${1:-}" = "rebuild-image" ] || ! image_exists; then
		build_image
	fi
}

run_opts=(
	--rm
	-e TERM=xterm-256color
	-e CGO_ENABLED=1
	-e GOTOOLCHAIN=local
	-e GOCACHE=/workspace/.gocache
	-e GOMODCACHE=/workspace/.gomodcache
	-e XDG_RUNTIME_DIR=/tmp
	-e HOME=/tmp
	-v "$ROOT_DIR:/workspace${volume_suffix}"
	-w /workspace
)

if [ "$ENGINE" = "podman" ]; then
	run_opts+=(--userns=keep-id)
else
	run_opts+=(--user "$(id -u):$(id -g)")
fi

mount_host_pipewire() {
	if [ -z "${XDG_RUNTIME_DIR:-}" ] || [ ! -S "${XDG_RUNTIME_DIR}/pipewire-0" ]; then
		echo "Host PipeWire socket not found; -live will not see devices." >&2
		return 0
	fi
	run_opts+=(
		-v "${XDG_RUNTIME_DIR}/pipewire-0:/tmp/pipewire-0"
	)
}

run_in_container() {
	$ENGINE run "${run_opts[@]}" "$IMAGE_NAME" "$@"
}

cmd="${1:-shell}"
shift || true

case "$cmd" in
rebuild-image)
	build_image
	;;
shell | "")
	ensure_image
	run_opts+=(-it)
	run_in_container bash
	;;
test)
	ensure_image
	run_in_container bash -c 'just test && just test-rt'
	;;
test-live)
	ensure_image
	run_in_container bash -c 'just test && just test-rt && just test-pipewire'
	;;
build)
	ensure_image
	run_in_container bash -c 'just build && just build-room'
	;;
build-live)
	ensure_image
	run_in_container bash -c 'just build && just build-room && just build-room-live'
	;;
live)
	ensure_image
	mount_host_pipewire
	run_opts+=(-it)
	run_in_container bash -c 'just build-room-live && ./jive-room -live'
	;;
distrobox)
	ensure_image
	if ! command -v distrobox >/dev/null 2>&1; then
		echo "distrobox is not installed on the host." >&2
		exit 1
	fi
	if distrobox list --no-color 2>/dev/null | awk '{print $1}' | grep -qx "$DISTROBOX_NAME"; then
		echo "Entering existing distrobox $DISTROBOX_NAME"
	else
		echo "Creating distrobox $DISTROBOX_NAME from $IMAGE_NAME"
		distrobox create --name "$DISTROBOX_NAME" --image "$IMAGE_NAME" --additional-flags "--userns=keep-id"
	fi
	exec distrobox enter "$DISTROBOX_NAME"
	;;
info)
	ensure_image
	echo "engine=$ENGINE image=$IMAGE_NAME volume_label=$volume_desc"
	run_in_container bash -c 'go version && pkg-config --modversion libpipewire-0.3 && just --version'
	;;
*)
	ensure_image
	run_in_container bash -c "$cmd $*"
	;;
esac
