#ifndef APPLICATION_H
#define APPLICATION_H

#include <circle/screen.h>
#include <circle/net/netsubsystem.h>
#include <circle/sched/scheduler.h>
#include <circle/timer.h>
#include <circle/logger.h>

#include "config/config.h"
#include "ui/display.h"

namespace mm {

enum class AppState {
    Initializing,
    Loading,
    Running,
    Error
};

class Grid;

class Application
{
public:
    Application(CScreenDevice* screen, CNetSubSystem* net,
                CScheduler* scheduler, CTimer* timer, CLogger* logger);
    ~Application();

    boolean Initialize();
    void Run();
    void Shutdown();

    AppState GetState() const { return m_state; }

private:
    boolean LoadConfig();
    boolean InitializeModules();
    boolean InitializeDataSources();
    boolean InitializeWidgets();

    void UpdateLoadingScreen(const char* moduleName);
    void ShowLoadingScreen(const char* text);
    void MainLoop();
    void Update();
    void Render();

    CScreenDevice*  m_pScreen;
    CNetSubSystem*  m_pNet;
    CScheduler*     m_pScheduler;
    CTimer*         m_pTimer;
    CLogger*        m_pLogger;

    AppState m_state;

    Config*         m_pConfig;
    Display*        m_pDisplay;
    Grid*           m_pGrid;

    unsigned m_nLastUpdateTime;
    static const unsigned UPDATE_INTERVAL_MS = 1000;
};

} // namespace mm

#endif // APPLICATION_H
