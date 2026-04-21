#
# Build image for Magic Mirror (Raspberry Pi Zero W, bare-metal via Circle OS).
#
# Provides the ARM cross-compiler and tools required by init.sh and make.
# init.sh + make are expected to be run at container runtime against the
# mounted source tree so that lib/ and deps/ persist on the host.
#

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc-arm-none-eabi \
        binutils-arm-none-eabi \
        libnewlib-arm-none-eabi \
        libstdc++-arm-none-eabi-newlib \
        build-essential \
        make \
        git \
        wget \
        ca-certificates \
        perl \
        python3 \
        bc \
        file \
        xxd \
        texinfo \
    && rm -rf /var/lib/apt/lists/*

# Let git operate on a bind-mounted repo owned by a different UID.
RUN git config --system --add safe.directory '*'

WORKDIR /src

CMD ["bash"]
