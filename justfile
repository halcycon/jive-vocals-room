# Jive Vocals - Just Commands

# PipeWire pkg-config on Fedora 1.6 injects -fno-strict-* which cgo rejects by default.
export CGO_CFLAGS_ALLOW := "-fno-strict-overflow|-fno-strict-aliasing|-D_REENTRANT"

# List commands
default:
    @just --list

# Check ffmpeg-statigo submodule is present
_check-submodule:
    #!/usr/bin/env bash
    if [ ! -f "third_party/ffmpeg-statigo/go.mod" ]; then
        echo "Error: ffmpeg-statigo submodule not initialised. Run 'just setup' first."
        exit 1
    fi
    if [ ! -f "third_party/ffmpeg-statigo/lib/$(go env GOOS)_$(go env GOARCH)/libffmpeg.a" ]; then
        echo "Error: ffmpeg-statigo library not downloaded. Run 'just setup' first."
        exit 1
    fi

# Get latest stable ffmpeg-statigo release tag from GitHub
_get-latest-tag:
    #!/usr/bin/env bash
    curl -s https://api.github.com/repos/linuxmatters/ffmpeg-statigo/releases | \
        jq -r '[.[] | select(.prerelease == false and .draft == false and (.tag_name | startswith("v")))][0].tag_name'

# Setup or update ffmpeg-statigo submodule and library
setup:
    #!/usr/bin/env bash
    set -e
    echo "Configuring git for submodule-friendly pulls..."
    git config pull.ff only
    git config submodule.recurse true

    TAG=$(just _get-latest-tag)
    if [ -z "$TAG" ] || [ "$TAG" = "null" ]; then
        echo "Error: Could not fetch latest release tag"
        exit 1
    fi

    if [ ! -f "third_party/ffmpeg-statigo/go.mod" ]; then
        echo "Initialising ffmpeg-statigo submodule..."
        git submodule update --init --recursive
    fi

    cd third_party/ffmpeg-statigo
    git fetch --no-tags origin "refs/tags/$TAG:refs/tags/$TAG"
    CURRENT=$(git describe --tags --exact-match 2>/dev/null || echo "")

    if [ "$CURRENT" = "$TAG" ]; then
        echo "ffmpeg-statigo already at latest version ($TAG)"
        cd ../..
    else
        if [ -n "$CURRENT" ]; then
            echo "Updating ffmpeg-statigo from $CURRENT to $TAG..."
        else
            echo "Setting up ffmpeg-statigo $TAG..."
        fi
        git checkout "$TAG"
        cd ../..
        rm -f third_party/ffmpeg-statigo/lib/*/libffmpeg.a
        git add third_party/ffmpeg-statigo
    fi

    echo "Checking ffmpeg-statigo libraries..."
    cd third_party/ffmpeg-statigo && go run ./cmd/download-lib
    cd ../..

    if git diff --cached --quiet third_party/ffmpeg-statigo; then
        echo "Setup complete!"
    else
        echo ""
        echo "Setup complete! Submodule updated to $TAG"
        echo "Don't forget to commit: git commit -m 'chore: update ffmpeg-statigo to $TAG'"
    fi

# Build jive-vocals
build: _check-submodule
    #!/usr/bin/env bash
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    echo "Building jive-vocals version: $VERSION"
    CGO_ENABLED=1 go build -ldflags="-X main.version=$VERSION" -o jive-vocals ./cmd/jive-vocals

# Build the additive offline room-calibration command
build-room: _check-submodule
    CGO_ENABLED=1 go build -o jive-room ./cmd/jive-room

# Build jive-room with PipeWire input capture (needs libpipewire development headers)
build-room-live: _check-submodule
    CGO_ENABLED=1 go build -tags pipewire -o jive-room ./cmd/jive-room

# Build jive-room with JACK input capture (needs JACK development headers)
build-room-jack: _check-submodule
    CGO_ENABLED=1 go build -tags jack -o jive-room ./cmd/jive-room

# JACK-tagged Go tests (headers required; hardware test runs only with JIVE_LIVE_JACK set)
test-jack: _check-submodule
    CGO_ENABLED=1 go test -tags jack ./internal/live/... ./cmd/jive-room

# JACK tests against a dummy JACK server (playback loopback; no hardware)
test-jack-dummy: _check-submodule
    #!/usr/bin/env bash
    set -euo pipefail
    jackd --no-realtime -n jive-dummy -d dummy -r 44100 -p 1024 >/tmp/jive-jackd-dummy.log 2>&1 &
    JACKD=$!
    trap 'kill $JACKD 2>/dev/null; wait $JACKD 2>/dev/null || true' EXIT
    sleep 1
    JACK_DEFAULT_SERVER=jive-dummy JIVE_JACK_DUMMY=1 CGO_ENABLED=1 go test -count=1 -tags jack ./internal/live/... ./cmd/jive-room

# Build the StudioLive Mac mini binary (JACK, glibc 2.39) in the Ubuntu 24.04 image
build-studiolive:
    JIVE_DEV_IMAGE=localhost/jive-room-ubuntu:24.04 JIVE_CONTAINERFILE=Containerfile.ubuntu ./scripts/dev-container.sh "just build-room-jack && just test-jack && just test-jack-dummy"

# Copy the JACK binary to the StudioLive Mac mini (default mixer@studiolive)
deploy-studiolive host="mixer@studiolive":
    ./scripts/deploy-studiolive.sh {{host}}

# Bridge apply-engine tests against fake_osclive (needs a python with python-osc; set JIVE_ROOM_BIN for the CLI test)
test-bridge python="python3":
    cd integrations/studiolive/bridge && {{python}} -m unittest -v test_bridge_dsp && rm -rf __pycache__

# Install the DSP-enabled bridge on the Mac mini (restarts the bridge and Open Stage Control)
deploy-studiolive-bridge host="mixer@studiolive":
    ./scripts/deploy-studiolive-bridge.sh {{host}}

# Regenerate and install the Open Stage Control session with the ROOM tab (restarts Open Stage Control)
deploy-studiolive-osc host="mixer@studiolive":
    ./scripts/deploy-studiolive-osc.sh {{host}}

# Offline C ring harness (no PipeWire)
test-rt:
    gcc -std=c11 -O2 -Wall -Wextra -o /tmp/jive-rt-ring-test internal/live/rt/ring.c internal/live/rt/harness/main.c
    /tmp/jive-rt-ring-test
    @rm -f /tmp/jive-rt-ring-test

# PipeWire-tagged Go tests (headers required; live hardware tests stay skipped)
test-pipewire: _check-submodule
    CGO_ENABLED=1 go test -tags pipewire ./internal/live/... ./cmd/jive-room

# Podman/Docker toolchain (PipeWire headers, Go, gcc). See docs/dev-container.md
container *args:
    ./scripts/dev-container.sh {{args}}

# Clean build artifacts and generated reports/data (.log, .json run-records, .jsonl sidecars)
clean:
    @rm -fv jive-vocals 2>/dev/null || true
    @rm -fv jive-room 2>/dev/null || true
    @rm -fv testdata/*-processed.* 2>/dev/null || true
    @rm -fv testdata/*-analysis.* 2>/dev/null || true
    @rm -fv testdata/LMP-*-stashed.* 2>/dev/null || true
    @rm -fv testdata/*.png 2>/dev/null || true
    @rm -fv testdata/*.txt 2>/dev/null || true

# Run tests
test: _check-submodule
    go test ./...

# Install jive-vocals to ~/.local/bin
install: build
    @mkdir -p ~/.local/bin 2>/dev/null || true
    @mv ./jive-vocals ~/.local/bin/jive-vocals
    @echo "Installed jive-vocals to ~/.local/bin/jive-vocals"
    @echo "Make sure ~/.local/bin is in your PATH"

# Make a VHS tape recording
vhs: build
    @rm -f testdata/LMP-81s-*-processed.flac
    @vhs ./jive-vocals.tape

# Show current version (from git tags or "dev" if no tags)
version:
    @git describe --tags --always --dirty 2>/dev/null || echo "dev"

# List recent releases
releases:
    @git tag --sort=-creatordate | head -10

# Show what would be in the next release changelog
changelog:
    #!/usr/bin/env bash
    LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
    if [ -n "$LATEST_TAG" ]; then
        echo "Changes since $LATEST_TAG:"
        git log $LATEST_TAG..HEAD --pretty=format:"* %s (%h)"
    else
        echo "No previous tags found. All commits:"
        git log --pretty=format:"* %s (%h)"
    fi

# Create a new release tag (requires VERSION=x.y.z)
release VERSION:
    #!/usr/bin/env bash
    set -e

    # Validate version format
    if ! echo "{{VERSION}}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
        echo "Error: VERSION must be in format x.y.z (e.g., 0.1.0)"
        exit 1
    fi

    # Check for uncommitted changes
    if ! git diff-index --quiet HEAD --; then
        echo "Error: Working directory has uncommitted changes"
        exit 1
    fi

    # Check if tag already exists
    if git rev-parse "{{VERSION}}" >/dev/null 2>&1; then
        echo "Error: Tag {{VERSION}} already exists"
        exit 1
    fi

    echo "Creating release {{VERSION}}..."
    git tag -a "{{VERSION}}" -m "Release {{VERSION}}"
    echo "✓ Tag {{VERSION}} created"
    echo ""
    echo "To publish the release:"
    echo "  git push origin {{VERSION}}"
    echo ""
    echo "This will trigger the GitHub Actions release workflow which will:"
    echo "  - Build binaries for all platforms"
    echo "  - Generate changelog from commits"
    echo "  - Create GitHub release with downloadable assets"

# Run linters
lint: _check-submodule
    @gocyclo -top 20 -avg -ignore '_test\.go$' .
    @ineffassign ./...
    @golangci-lint run
    @actionlint
