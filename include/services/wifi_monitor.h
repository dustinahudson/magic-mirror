#ifndef WIFI_MONITOR_H
#define WIFI_MONITOR_H

#include <circle/types.h>

class CTimer;
class CNetSubSystem;
class CBcm4343Device;

namespace mm {

// Monitors wifi health via a periodic active DNS probe. The probe is the
// ground-truth test for "do packets actually route end-to-end right now";
// fetch failures (HTTP 5xx, parse errors, remote data issues, etc.) are
// intentionally ignored because those aren't wifi problems.
//
// State machine:
//   Healthy  -> Degraded on N consecutive probe failures
//   Degraded -> Kicked   after dwell timeout (sends disassoc to firmware)
//   Kicked   -> Healthy  if probe recovers after reassociation
//   Kicked   -> Dead     if still failing after dwell timeout
//   Dead     -> (reboot requested by kernel loop)
class WiFiMonitor
{
public:
    enum State {
        Healthy,
        Degraded,
        Kicked,
        Dead
    };

    WiFiMonitor(CNetSubSystem* pNet, CBcm4343Device* pWLAN, CTimer* pTimer);

    // Called every main-loop tick. Drives probing and state transitions.
    // May block the caller for up to ~3s while a DNS probe times out.
    void Tick();

    bool RebootRequested() const { return m_state == Dead; }
    State GetState() const { return m_state; }

private:
    bool Probe();
    void EnterState(State newState);
    unsigned Now() const;

    CNetSubSystem*  m_pNet;
    CBcm4343Device* m_pWLAN;
    CTimer*         m_pTimer;

    State    m_state;
    unsigned m_stateEnteredAt;       // uptime seconds
    unsigned m_lastProbeAt;          // uptime seconds
    unsigned m_consecutiveFailures;
};

} // namespace mm

#endif // WIFI_MONITOR_H
