#include "services/wifi_monitor.h"
#include <circle/timer.h>
#include <circle/logger.h>
#include <circle/net/netsubsystem.h>
#include <circle/net/dnsclient.h>
#include <circle/net/ipaddress.h>
#include <wlan/bcm4343.h>

static const char FromWiFi[] = "wifi";

namespace mm {

// Tunables — all times in seconds.
//
// Probe cadence: slow in Healthy (cheap keepalive), fast once we're
// suspicious. Probe calls block up to ~3s on failure, so don't crank
// the rate too high.
static const unsigned PROBE_INTERVAL_HEALTHY_SEC  = 30;
static const unsigned PROBE_INTERVAL_SUSPECT_SEC  = 10;

// Consecutive probe failures required to enter Degraded.
static const unsigned FAILURE_THRESHOLD = 2;

// Dwell timers for escalation.
static const unsigned DEGRADED_TO_KICKED_SEC = 60;
static const unsigned KICKED_SETTLE_SEC      = 15;   // give wpa_s time to reassoc
static const unsigned KICKED_TO_DEAD_SEC     = 60;

// Target for active probe. pool.ntp.org is already used at boot, requires
// real DNS resolution (not a literal IP), and is reliably reachable.
static const char* PROBE_HOSTNAME = "pool.ntp.org";

WiFiMonitor::WiFiMonitor(CNetSubSystem* pNet, CBcm4343Device* pWLAN, CTimer* pTimer)
    : m_pNet(pNet),
      m_pWLAN(pWLAN),
      m_pTimer(pTimer),
      m_state(Healthy),
      m_stateEnteredAt(0),
      m_lastProbeAt(0),
      m_consecutiveFailures(0)
{
    unsigned now = Now();
    m_stateEnteredAt = now;
    // Delay first probe by a few seconds after construction so the network
    // has a moment to fully settle past DHCP.
    m_lastProbeAt = now - PROBE_INTERVAL_HEALTHY_SEC + 10;
}

unsigned WiFiMonitor::Now() const
{
    return m_pTimer->GetUptime();
}

static const char* StateName(WiFiMonitor::State s)
{
    switch (s) {
        case WiFiMonitor::Healthy:  return "Healthy";
        case WiFiMonitor::Degraded: return "Degraded";
        case WiFiMonitor::Kicked:   return "Kicked";
        case WiFiMonitor::Dead:     return "Dead";
    }
    return "?";
}

void WiFiMonitor::EnterState(State newState)
{
    if (m_state == newState) {
        return;
    }
    CLogger::Get()->Write(FromWiFi, LogNotice, "state %s -> %s",
                          StateName(m_state), StateName(newState));
    m_state = newState;
    m_stateEnteredAt = Now();
}

bool WiFiMonitor::Probe()
{
    if (m_pNet == nullptr) {
        return false;
    }
    CDNSClient dns(m_pNet);
    CIPAddress ip;
    return !!dns.Resolve(PROBE_HOSTNAME, &ip);
}

void WiFiMonitor::Tick()
{
    unsigned now = Now();
    unsigned dwell = now - m_stateEnteredAt;

    // Decide whether to probe this tick. In Kicked we hold off until the
    // reassociation settle window has passed; in Dead we stop entirely.
    bool shouldProbe = false;
    unsigned interval = (m_consecutiveFailures > 0)
                        ? PROBE_INTERVAL_SUSPECT_SEC
                        : PROBE_INTERVAL_HEALTHY_SEC;

    switch (m_state) {
        case Healthy:
        case Degraded:
            shouldProbe = (now - m_lastProbeAt) >= interval;
            break;
        case Kicked:
            shouldProbe = dwell >= KICKED_SETTLE_SEC &&
                          (now - m_lastProbeAt) >= PROBE_INTERVAL_SUSPECT_SEC;
            break;
        case Dead:
            break;
    }

    if (shouldProbe) {
        m_lastProbeAt = now;
        bool ok = Probe();

        if (ok) {
            if (m_consecutiveFailures > 0 || m_state != Healthy) {
                CLogger::Get()->Write(FromWiFi, LogNotice,
                                      "probe ok, resetting (was %s, failures=%u)",
                                      StateName(m_state), m_consecutiveFailures);
            }
            m_consecutiveFailures = 0;
            if (m_state != Healthy) {
                EnterState(Healthy);
            }
        } else {
            m_consecutiveFailures++;
            CLogger::Get()->Write(FromWiFi, LogWarning,
                                  "probe failed (count=%u, state=%s)",
                                  m_consecutiveFailures, StateName(m_state));
            if (m_state == Healthy && m_consecutiveFailures >= FAILURE_THRESHOLD) {
                EnterState(Degraded);
            }
        }
    }

    // State-based escalation, independent of whether we probed this tick.
    switch (m_state) {
        case Healthy:
            break;

        case Degraded:
            if (dwell >= DEGRADED_TO_KICKED_SEC) {
                CLogger::Get()->Write(FromWiFi, LogWarning,
                                      "degraded for %us, sending disassoc", dwell);
                if (m_pWLAN) {
                    m_pWLAN->Control("disassoc 0");
                }
                EnterState(Kicked);
            }
            break;

        case Kicked:
            if (dwell >= KICKED_TO_DEAD_SEC) {
                CLogger::Get()->Write(FromWiFi, LogError,
                                      "kick did not recover after %us, rebooting", dwell);
                EnterState(Dead);
            }
            break;

        case Dead:
            break;
    }
}

} // namespace mm
