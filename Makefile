# Magic Mirror — build entry points.
#
# Two independent products, matching the two update tiers:
#
#   binary   the Go application, shipped as mm.current on the FAT partition
#   image    the Buildroot kernel+initramfs, shipped as kernel.img
#
# An app release rebuilds only the first, which is why `make binary` is fast
# and `make image` is not.

VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILDROOT_VER?= 2024.02.9
BUILDROOT_DIR?= board/buildroot
DIST         ?= dist
BR_EXTERNAL  := $(CURDIR)/board

# ARMv6 with hard float: BCM2835 is an ARM1176JZF-S, which is why a Pi Zero
# cannot run the armv7 builds everything else ships.
GOOS   := linux
GOARCH := arm
GOARM  := 6

LDFLAGS := -s -w -X main.version=$(VERSION)

# Explicit package list rather than ./... — after a build, board/buildroot
# contains gcc's own Go testsuite, thousands of deliberately-malformed files
# that break any recursive walk.
PKGS := ./cmd/... ./internal/... ./assets/...

.PHONY: all
all: binary

# --- Application ---

.PHONY: binary
binary: $(DIST)/magicmirror-armv6

$(DIST)/magicmirror-armv6: $(shell find . -name '*.go' -not -path './board/*') go.mod
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM=$(GOARM) \
		go build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/magicmirror
	@echo "built $@ ($(VERSION), $$(du -h $@ | cut -f1))"

.PHONY: host
host:
	@mkdir -p $(DIST)
	go build -ldflags "-X main.version=$(VERSION)" -o $(DIST)/magicmirror ./cmd/magicmirror

# Run the mirror locally with a live browser preview. This is the loop that
# makes layout iteration seconds rather than an SD card flash.
.PHONY: run
run: host
	$(DIST)/magicmirror -preview :8080 -config config.json -v

.PHONY: test
test:
	go test $(PKGS)

.PHONY: lint
lint:
	go vet $(PKGS)
	gofmt -l . | grep -v '^board/' || true

# --- OS image ---

$(BUILDROOT_DIR):
	@echo "fetching buildroot $(BUILDROOT_VER)"
	@mkdir -p $(dir $(BUILDROOT_DIR))
	curl -sSL https://buildroot.org/downloads/buildroot-$(BUILDROOT_VER).tar.gz \
		| tar xz -C $(dir $(BUILDROOT_DIR))
	mv $(dir $(BUILDROOT_DIR))buildroot-$(BUILDROOT_VER) $(BUILDROOT_DIR)

.PHONY: buildroot
buildroot: $(BUILDROOT_DIR)

.PHONY: defconfig
defconfig: $(BUILDROOT_DIR)
	$(MAKE) -C $(BUILDROOT_DIR) BR2_EXTERNAL=$(BR_EXTERNAL) magicmirror_defconfig

# The long one: a full toolchain plus kernel build. Expect an hour or more on
# a first run, minutes on a rebuild.
.PHONY: image
image: binary defconfig
	$(MAKE) -C $(BUILDROOT_DIR) BR2_EXTERNAL=$(BR_EXTERNAL)

.PHONY: menuconfig
menuconfig: $(BUILDROOT_DIR)
	$(MAKE) -C $(BUILDROOT_DIR) BR2_EXTERNAL=$(BR_EXTERNAL) menuconfig

.PHONY: savedefconfig
savedefconfig: $(BUILDROOT_DIR)
	$(MAKE) -C $(BUILDROOT_DIR) BR2_EXTERNAL=$(BR_EXTERNAL) savedefconfig \
		BR2_DEFCONFIG=$(BR_EXTERNAL)/configs/magicmirror_defconfig

# --- Card ---

.PHONY: card
card:
	@test -n "$(DEV)" || (echo "usage: make card DEV=/dev/sdX (or MOUNT=/media/you/BOOT)"; exit 1)
	./scripts/write-card.sh "$(DEV)"

.PHONY: clean
clean:
	rm -rf $(DIST)

.PHONY: distclean
distclean: clean
	rm -rf $(BUILDROOT_DIR)
