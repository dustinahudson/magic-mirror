#
# Makefile - Magic Mirror for Raspberry Pi Zero W
# Uses circle-stdlib for TLS/HTTPS support
#

# Default target
.PHONY: all
all: kernel.img

# circle-stdlib paths
CIRCLE_STDLIB_DIR = $(CURDIR)/lib/circle-stdlib
CIRCLEHOME = $(CIRCLE_STDLIB_DIR)/libs/circle
NEWLIBDIR = $(CIRCLE_STDLIB_DIR)/install/arm-none-circle
MBEDTLS_DIR = $(CIRCLE_STDLIB_DIR)/libs/mbedtls

# Extra include paths - order matters for type compatibility
# Newlib headers must come first to avoid time_t conflicts
EXTRAINCLUDE = -isystem $(NEWLIBDIR)/include \
               -I include \
               -I src/ui/icons \
               -I $(CIRCLE_STDLIB_DIR)/include \
               -I $(MBEDTLS_DIR)/include \
               -I $(CIRCLEHOME)/addon/fatfs \
               -I $(CIRCLEHOME)/addon/SDCard \
               -I $(CIRCLEHOME)/addon/wlan \
               -I $(CIRCLEHOME)/addon \
               -I $(CIRCLEHOME)/addon/lvgl \
               -I $(CIRCLEHOME)/addon/lvgl/lvgl

# Extra defines
DEFINE += -DUSE_HDMI -DSCREEN_WIDTH=1920 -DSCREEN_HEIGHT=1080
DEFINE += -DMBEDTLS_CONFIG_FILE='<circle-mbedtls/config-circle-mbedtls.h>'
# Increase kernel max size from default 2MB to 4MB for larger assets
DEFINE += -DKERNEL_MAX_SIZE=0x400000
DEFINE += -DAPP_VERSION='"v0.5.0"'

# Object files
OBJS = src/main.o \
       src/core/kernel.o \
       src/core/application.o \
       src/config/config.o \
       src/ui/display.o \
       src/ui/grid.o \
       src/ui/icons/weather_icons.o \
       src/modules/widgets/widget_base.o \
       src/modules/widgets/datetime_widget.o \
       src/modules/widgets/weather_widget.o \
       src/modules/widgets/calendar_widget.o \
       src/modules/widgets/upcoming_events_widget.o \
       src/services/http_client.o \
       src/services/weather_service.o \
       src/services/geocoding_service.o \
       src/services/calendar_service.o \
       src/services/ics_stream_parser.o \
       src/services/update_service.o \
       src/services/file_logger.o

# Libraries - order matters for linking
LIBS = $(CIRCLE_STDLIB_DIR)/src/circle-mbedtls/libcircle-mbedtls.a \
       $(MBEDTLS_DIR)/library/libmbedtls.a \
       $(MBEDTLS_DIR)/library/libmbedx509.a \
       $(MBEDTLS_DIR)/library/libmbedcrypto.a \
       $(CIRCLEHOME)/addon/lvgl/liblvgl.a \
       $(CIRCLEHOME)/addon/wlan/hostap/wpa_supplicant/libwpa_supplicant.a \
       $(CIRCLEHOME)/addon/wlan/libwlan.a \
       $(CIRCLEHOME)/addon/fatfs/libfatfs.a \
       $(CIRCLEHOME)/addon/SDCard/libsdcard.a \
       $(CIRCLEHOME)/lib/usb/libusb.a \
       $(CIRCLEHOME)/lib/input/libinput.a \
       $(CIRCLEHOME)/lib/fs/libfs.a \
       $(CIRCLEHOME)/lib/net/libnet.a \
       $(CIRCLEHOME)/lib/sched/libsched.a \
       $(CIRCLEHOME)/lib/libcircle.a \
       $(NEWLIBDIR)/lib/libcirclenewlib.a \
       $(NEWLIBDIR)/lib/libc.a \
       $(NEWLIBDIR)/lib/libm.a

EXTRACLEAN = $(OBJS) $(OBJS:.o=.d)

include $(CIRCLEHOME)/Rules.mk

INCLUDE += $(EXTRAINCLUDE)

-include $(DEPS)
