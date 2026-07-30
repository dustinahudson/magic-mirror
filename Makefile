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

# Embedded assets are prerequisites too. They are compiled into the binary
# with go:embed, so a changed ui.html or icon must mark it stale — listing
# only *.go meant editing the web UI produced a "successful" deploy that
# shipped the previous build.
GOFILES   := $(shell find . -name '*.go' -not -path './board/*')
EMBEDDED  := $(shell find assets internal/web -type f -not -name '*.go' 2>/dev/null)

$(DIST)/magicmirror-armv6: $(GOFILES) $(EMBEDDED) go.mod
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM=$(GOARM) \
		go build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/magicmirror
	@echo "built $@ ($(VERSION), $$(du -h $@ | cut -f1))"

.PHONY: host
host: $(GOFILES) $(EMBEDDED)
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

# --- Deploy over the network ---
#
# The iteration loop that replaces swapping the SD card. An app change is a
# 9MB copy and a sub-second respawn; only a kernel or init-script change
# needs a reboot, and neither needs a card reader.

MIRROR ?= magicmirror.local

.PHONY: deploy
deploy: binary
	@echo "deploying to $(MIRROR)"
	scp -O $(DIST)/magicmirror-armv6 root@$(MIRROR):/boot/mm.new
	# Swap and restart atomically-ish: keep the outgoing binary as the
	# rollback copy, rename the new one into place, then exit the app so
	# init respawns it. No reboot involved.
	ssh root@$(MIRROR) 'cp /boot/mm.current /boot/mm.previous 2>/dev/null; \
		mv /boot/mm.new /boot/mm.current && chmod +x /boot/mm.current && sync && \
		killall magicmirror mm.current 2>/dev/null; true'
	@echo "deployed; the app should be back within a second or two"

# A kernel or init-script change needs the whole image and a reboot.
.PHONY: deploy-os
deploy-os: image
	@echo "deploying kernel to $(MIRROR)"
	scp -O board/buildroot/output/images/boot/kernel.img root@$(MIRROR):/boot/kernel.new
	ssh root@$(MIRROR) 'cp /boot/kernel.img /boot/kernel.prev.img 2>/dev/null; \
		mv /boot/kernel.new /boot/kernel.img && sync && reboot'
	@echo "rebooting; back in about 15 seconds"

.PHONY: logs
logs:
	ssh root@$(MIRROR) 'tail -f /boot/logs/mm.log'

.PHONY: shell
shell:
	ssh root@$(MIRROR)

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
