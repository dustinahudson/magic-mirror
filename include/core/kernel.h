#ifndef KERNEL_H
#define KERNEL_H

#include <circle/actled.h>
#include <circle/koptions.h>
#include <circle/devicenameservice.h>
#include <circle/screen.h>
#include <circle/serial.h>
#include <circle/exceptionhandler.h>
#include <circle/interrupt.h>
#include <circle/timer.h>
#include <circle/logger.h>
#include <circle/sched/scheduler.h>
#include <circle/net/netsubsystem.h>
#include <circle/usb/usbhcidevice.h>
#include <circle/types.h>

// LVGL for display management
#include <lvgl/lvgl.h>

// SD Card and filesystem
#include <SDCard/emmc.h>
#include <ff.h>

// WLAN support
#include <wlan/bcm4343.h>
#include <wlan/hostap/wpa_supplicant/wpasupplicant.h>

// Forward declare TLS support (full include in .cpp to avoid time_t conflicts)
namespace CircleMbedTLS {
    class CTLSSimpleSupport;
}

// USB CDC gadget for serial console (disabled - causes crash on Pi Zero W)
// #define USE_USB_SERIAL_GADGET

#ifdef USE_USB_SERIAL_GADGET
#include <circle/usb/gadget/usbcdcgadget.h>
#endif

enum TShutdownMode
{
    ShutdownNone,
    ShutdownHalt,
    ShutdownReboot
};

class CKernel
{
public:
    CKernel();
    ~CKernel();

    boolean Initialize();
    TShutdownMode Run();

private:
    // Do not change this order
    CActLED             m_ActLED;
    CKernelOptions      m_Options;
    CDeviceNameService  m_DeviceNameService;
    CScreenDevice       m_Screen;
    CSerialDevice       m_Serial;
    CExceptionHandler   m_ExceptionHandler;
    CInterruptSystem    m_Interrupt;
    CTimer              m_Timer;
    CLogger             m_Logger;
    CScheduler          m_Scheduler;
    CUSBHCIDevice       m_USBHCI;
    CLVGL               m_LVGL;
#ifdef USE_USB_SERIAL_GADGET
    CUSBCDCGadget       m_USBSerial;
#endif
    CEMMCDevice         m_EMMC;
    FATFS               m_FileSystem;
    CBcm4343Device      m_WLAN;
    CNetSubSystem       m_Net;
    CWPASupplicant      m_WPASupplicant;
    CircleMbedTLS::CTLSSimpleSupport* m_pTLS;

    bool m_bNetworkReady;
};

#endif // KERNEL_H
