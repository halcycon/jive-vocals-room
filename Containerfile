# Toolchain image for jive-room / live PipeWire development.
# Source is bind-mounted at runtime; this image only carries compilers and headers.
#
# Fedora (not Debian) because third_party/ffmpeg-statigo ships a prebuilt
# libffmpeg.a linked against a current glibc (__isoc23_*). Bookworm cannot link it.
FROM registry.fedoraproject.org/fedora:44

RUN dnf -y install --setopt=install_weak_deps=False \
        golang \
        gcc \
        git \
        glibc-devel \
        jq \
        just \
        libstdc++ \
        libstdc++-devel \
        gcc-c++ \
        pipewire-devel \
        pkgconf-pkg-config \
    && dnf clean all \
    && go version \
    && pkg-config --exists libpipewire-0.3 \
    && pkg-config --modversion libpipewire-0.3 \
    && pkg-config --cflags --libs libpipewire-0.3 \
    && just --version

WORKDIR /workspace

ENV CGO_ENABLED=1 \
    GOTOOLCHAIN=local \
    CGO_CFLAGS_ALLOW="-fno-strict-overflow|-fno-strict-aliasing|-D_REENTRANT"
