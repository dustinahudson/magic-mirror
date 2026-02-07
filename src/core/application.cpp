#include "core/application.h"
#include "ui/display.h"
#include "ui/grid.h"
#include <ff.h>
#include <circle/util.h>
#include <circle/string.h>

static const char FromApp[] = "app";

// File logging for debugging without serial
static FIL g_LogFile;
static bool g_bLogFileOpen = false;

// Disable file logging to test if I/O is causing crash
#define DISABLE_FILE_LOGGING 1

#if !DISABLE_FILE_LOGGING
static void LogString(const char* msg)
{
    if (!g_bLogFileOpen) {
        if (f_open(&g_LogFile, "SD:/debug.log", FA_WRITE | FA_CREATE_ALWAYS) == FR_OK) {
            g_bLogFileOpen = true;
        } else {
            return;
        }
    }

    UINT written;
    f_write(&g_LogFile, msg, strlen(msg), &written);
    f_write(&g_LogFile, "\n", 1, &written);
    f_sync(&g_LogFile);
}

static void LogInt(const char* prefix, int value, const char* suffix = "")
{
    CString str;
    str.Format("%s%d%s", prefix, value, suffix);
    LogString((const char*)str);
}

static void CloseLogFile()
{
    if (g_bLogFileOpen) {
        LogString("=== Log closed ===");
        f_close(&g_LogFile);
        g_bLogFileOpen = false;
    }
}
#else
// No-op logging
static void LogString(const char*) {}
static void LogInt(const char*, int, const char* = "") {}
static void CloseLogFile() {}
#endif

static void LogPtr(const char* prefix, void* ptr)
{
    (void)prefix;
    (void)ptr;
}

namespace mm {

Application::Application(CScreenDevice* screen, CNetSubSystem* net,
                         CScheduler* scheduler, CTimer* timer, CLogger* logger)
    : m_pScreen(screen),
      m_pNet(net),
      m_pScheduler(scheduler),
      m_pTimer(timer),
      m_pLogger(logger),
      m_state(AppState::Initializing),
      m_pConfig(0),
      m_pDisplay(0),
      m_pGrid(0),
      m_nLastUpdateTime(0)
{
}

Application::~Application()
{
    Shutdown();
}

boolean Application::Initialize()
{
    LogString("=== Application Initialize ===");
    m_pLogger->Write(FromApp, LogNotice, "Initializing application...");

    // Load configuration
    LogString("Loading config...");
    m_pLogger->Write(FromApp, LogNotice, "Loading config...");
    m_pConfig = new Config;
    if (!LoadConfig()) {
        LogString("ERROR: Failed to load configuration");
        m_pLogger->Write(FromApp, LogError, "Failed to load configuration");
        m_state = AppState::Error;
        return FALSE;
    }
    LogString("Config loaded");
    m_pLogger->Write(FromApp, LogNotice, "Config loaded");

    // Initialize display
    LogString("Initializing display...");
    m_pLogger->Write(FromApp, LogNotice, "Initializing display...");
    m_pDisplay = new Display(m_pScreen);
    if (!m_pDisplay->Initialize()) {
        LogString("ERROR: Failed to initialize display");
        m_pLogger->Write(FromApp, LogError, "Failed to initialize display");
        m_state = AppState::Error;
        return FALSE;
    }
    LogInt("Display initialized: ", m_pDisplay->GetWidth());
    m_pLogger->Write(FromApp, LogNotice, "Display initialized: %dx%d",
                     m_pDisplay->GetWidth(), m_pDisplay->GetHeight());

    // Initialize grid
    LogString("Initializing grid...");
    m_pLogger->Write(FromApp, LogNotice, "Initializing grid...");
    m_pGrid = new Grid(m_pDisplay, m_pConfig->grid);
    LogString("Grid initialized");
    m_pLogger->Write(FromApp, LogNotice, "Grid initialized");

    // Show loading screen
    LogString("Showing loading screen...");
    m_pLogger->Write(FromApp, LogNotice, "Showing loading screen...");
    m_state = AppState::Loading;
    ShowLoadingScreen("Initializing...");

    // For now, just transition to running
    m_state = AppState::Running;

    LogString("Application initialized - entering main loop");
    m_pLogger->Write(FromApp, LogNotice, "Application initialized");
    return TRUE;
}

boolean Application::LoadConfig()
{
    return Config::LoadFromFile("SD:/config/config.json", m_pConfig);
}

boolean Application::InitializeModules()
{
    return TRUE;
}

boolean Application::InitializeDataSources()
{
    return TRUE;
}

boolean Application::InitializeWidgets()
{
    return TRUE;
}

void Application::UpdateLoadingScreen(const char* moduleName)
{
    ShowLoadingScreen(moduleName);
}

void Application::ShowLoadingScreen(const char* text)
{
    m_pLogger->Write(FromApp, LogNotice, "ShowLoadingScreen: clearing...");
    m_pDisplay->Clear(Color::Black());

    m_pLogger->Write(FromApp, LogNotice, "ShowLoadingScreen: drawing rect...");
    // Draw "Loading" text indicator (simple rectangle for now)
    int centerX = m_pDisplay->GetWidth() / 2;
    int centerY = m_pDisplay->GetHeight() / 2;

    Rect loadingRect = {centerX - 100, centerY - 20, 200, 40};
    m_pDisplay->FillRect(loadingRect, Color::Gray(40));
    m_pDisplay->DrawRect(loadingRect, Color::White());

    m_pLogger->Write(FromApp, LogNotice, "ShowLoadingScreen: presenting...");
    m_pDisplay->Present();
    m_pLogger->Write(FromApp, LogNotice, "ShowLoadingScreen: done");
}

void Application::Run()
{
    m_pLogger->Write(FromApp, LogNotice, "Entering main loop");

    // Render once at startup to test if static display works with network
    Render();

    while (m_state == AppState::Running) {
        // Don't update display - just sleep to test if network causes blue screen
        // when we're NOT writing to framebuffer
        m_pScheduler->MsSleep(100);
    }
}

void Application::MainLoop()
{
    static int frameCount = 0;
    static int loopCount = 0;
    static bool firstFrame = true;
    unsigned currentTime = m_pTimer->GetClockTicks();

    loopCount++;

    // Log heartbeat every 10000 loops to verify main loop continues
    if (loopCount % 10000 == 0) {
        LogInt("Loop ", loopCount);
    }

    if (currentTime - m_nLastUpdateTime >= UPDATE_INTERVAL_MS * 1000) {
        if (firstFrame) {
            LogString("First frame starting");
            firstFrame = false;
        }

        frameCount++;
        LogInt("F", frameCount);

        Update();
        Render();
        m_nLastUpdateTime = currentTime;
    }

    m_pScheduler->MsSleep(10);
}

void Application::Update()
{
    // TODO: Update widgets when implemented
}

void Application::Render()
{
    m_pDisplay->Clear(Color::Black());

    int w = m_pDisplay->GetWidth();
    int h = m_pDisplay->GetHeight();

    m_pDisplay->DrawRect({10, 10, w - 20, h - 20}, Color::White());
    m_pGrid->DrawDebugGrid(Color::Gray(30));
    m_pDisplay->Present();
}

void Application::Shutdown()
{
    LogString("=== Shutdown ===");
    m_pLogger->Write(FromApp, LogNotice, "Shutting down");
    CloseLogFile();

    if (m_pGrid) {
        delete m_pGrid;
        m_pGrid = 0;
    }

    if (m_pDisplay) {
        delete m_pDisplay;
        m_pDisplay = 0;
    }

    if (m_pConfig) {
        delete m_pConfig;
        m_pConfig = 0;
    }
}

} // namespace mm
