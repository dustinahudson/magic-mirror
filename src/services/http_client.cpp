#include "services/http_client.h"
#include <circle/net/dnsclient.h>
#include <circle/net/in.h>
#include <circle/logger.h>
#include <circle/timer.h>
#include <circle/sched/scheduler.h>
#include <fatfs/ff.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>

#ifndef HTTP_PORT
#define HTTP_PORT   80
#endif
#ifndef HTTPS_PORT
#define HTTPS_PORT  443
#endif

static const char FromHttpClient[] = "http";

// Helper: parse Location header from HTTP response headers
static bool ParseLocationHeader(const char* headerBuf, char* redirectUrl, size_t maxLen)
{
    const char* p = headerBuf;
    while (*p) {
        if ((*p == 'L' || *p == 'l') &&
            strncasecmp(p, "Location:", 9) == 0) {
            p += 9;
            while (*p == ' ' || *p == '\t') p++;
            const char* end = strchr(p, '\r');
            if (!end) end = strchr(p, '\n');
            if (end) {
                size_t len = end - p;
                if (len < maxLen) {
                    strncpy(redirectUrl, p, len);
                    redirectUrl[len] = '\0';
                    return true;
                }
            }
            return false;
        }
        const char* nl = strchr(p, '\n');
        if (!nl) break;
        p = nl + 1;
    }
    return false;
}

namespace mm {

// Static members
CBcmWatchdog* HttpClient::s_pWatchdog = nullptr;
static HttpResponse s_sharedResponse;

HttpResponse* HttpClient::GetSharedResponse()
{
    return &s_sharedResponse;
}

HttpClient::HttpClient(CNetSubSystem* pNet, CircleMbedTLS::CTLSSimpleSupport* pTLS)
    : m_pNet(pNet),
      m_pTLS(pTLS)
{
}

HttpClient::~HttpClient()
{
}

bool HttpClient::IsNetworkAvailable()
{
    return m_pNet && m_pNet->IsRunning();
}

void HttpClient::SetWatchdog(CBcmWatchdog* pWatchdog)
{
    s_pWatchdog = pWatchdog;
}

unsigned HttpClient::ComputeDeadline(unsigned timeoutMs)
{
    return CTimer::Get()->GetTicks() + (timeoutMs * HZ / 1000);
}

bool HttpClient::IsDeadlineExpired(unsigned deadlineTicks)
{
    return (int)(CTimer::Get()->GetTicks() - deadlineTicks) >= 0;
}

int HttpClient::ReceiveWithTimeout(CSocket& socket, void* buf, unsigned size,
                                   unsigned deadlineTicks)
{
    while (!IsDeadlineExpired(deadlineTicks)) {
        int n = socket.Receive(buf, size, MSG_DONTWAIT);
        if (n != 0) return n;  // data received (>0) or closed/error (<0)
        CScheduler::Get()->Yield();
    }
    return 0;  // deadline expired
}

// Get() delegates to GetRaw() — all HTTP now goes through timeout-protected raw sockets
bool HttpClient::Get(const char* url, HttpResponse* response)
{
    return GetRaw(url, response);
}

bool HttpClient::Get(const char* host, const char* path, bool useSSL, HttpResponse* response)
{
    char url[1024];
    snprintf(url, sizeof(url), "%s://%s%s", useSSL ? "https" : "http", host, path);
    return GetRaw(url, response);
}

bool HttpClient::ParseUrl(const char* url, char* host, size_t hostLen,
                          char* path, size_t pathLen, bool* useSSL)
{
    const char* ptr = url;
    *useSSL = false;

    // Check for protocol
    if (strncmp(ptr, "https://", 8) == 0) {
        *useSSL = true;
        ptr += 8;
    } else if (strncmp(ptr, "http://", 7) == 0) {
        ptr += 7;
    }

    // Find end of host (first / or end of string)
    const char* pathStart = strchr(ptr, '/');
    size_t hostLength;

    if (pathStart) {
        hostLength = pathStart - ptr;
        strncpy(path, pathStart, pathLen - 1);
        path[pathLen - 1] = '\0';
    } else {
        hostLength = strlen(ptr);
        strncpy(path, "/", pathLen - 1);
        path[pathLen - 1] = '\0';
    }

    if (hostLength >= hostLen) {
        return false;
    }

    strncpy(host, ptr, hostLength);
    host[hostLength] = '\0';

    return true;
}

bool HttpClient::DownloadFile(const char* url, const char* sdPath, unsigned timeoutMs)
{
    if (s_pWatchdog) {
        s_pWatchdog->Start(15);
    }

    unsigned deadlineTicks = ComputeDeadline(timeoutMs);
    return DownloadFileInternal(url, sdPath, 5, deadlineTicks);
}

bool HttpClient::DownloadFileInternal(const char* url, const char* sdPath,
                                       int redirectsLeft, unsigned deadlineTicks)
{
    if (redirectsLeft <= 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: too many redirects");
        return false;
    }

    if (!IsNetworkAvailable()) {
        CLogger::Get()->Write(FromHttpClient, LogWarning, "DownloadFile: network unavailable, skipping");
        return false;
    }

    char host[256];
    char path[1024];
    bool useSSL = false;

    if (!ParseUrl(url, host, sizeof(host), path, sizeof(path), &useSSL)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: bad URL: %s", url);
        return false;
    }

    unsigned port = useSSL ? HTTPS_PORT : HTTP_PORT;

    // DNS resolve
    CIPAddress ip;
    CDNSClient dns(m_pNet);

    CLogger::Get()->Write(FromHttpClient, LogDebug, "DownloadFile: resolving %s", host);
    if (!dns.Resolve(host, &ip)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: DNS failed for %s", host);
        return false;
    }

    if (IsDeadlineExpired(deadlineTicks)) {
        CLogger::Get()->Write(FromHttpClient, LogWarning, "DownloadFile: deadline expired after DNS");
        return false;
    }

    // Build HTTP request
    char request[1536];
    snprintf(request, sizeof(request),
             "GET %s HTTP/1.0\r\n"
             "Host: %s\r\n"
             "User-Agent: MagicMirror/1.0\r\n"
             "Accept: application/octet-stream\r\n"
             "Connection: close\r\n"
             "\r\n",
             path, host);

    CLogger::Get()->Write(FromHttpClient, LogDebug, "DownloadFile: connecting to %s:%u (SSL=%d)",
                          host, port, useSSL ? 1 : 0);

    // Create socket and connect
    if (useSSL) {
        CircleMbedTLS::CTLSSimpleClientSocket tlsSocket(m_pTLS, IPPROTO_TCP);
        if (tlsSocket.Setup(host) != 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: TLS setup failed");
            return false;
        }
        if (tlsSocket.Connect(ip, port) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: TLS connect failed");
            return false;
        }

        if (IsDeadlineExpired(deadlineTicks)) {
            CLogger::Get()->Write(FromHttpClient, LogWarning, "DownloadFile: deadline expired after TLS connect");
            return false;
        }

        if (tlsSocket.Send(request, strlen(request), 0) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: TLS send failed");
            return false;
        }

        // Receive headers byte by byte
        char headerBuf[4096];
        unsigned headerLen = 0;
        bool headersComplete = false;

        while (headerLen < sizeof(headerBuf) - 1) {
            if (IsDeadlineExpired(deadlineTicks)) {
                CLogger::Get()->Write(FromHttpClient, LogWarning,
                    "DownloadFile: deadline expired during header receive (%u bytes)", headerLen);
                return false;
            }

            int n = tlsSocket.Receive((u8*)&headerBuf[headerLen], 1, 0);
            if (n <= 0) break;
            headerLen++;

            // Check for \r\n\r\n
            if (headerLen >= 4 &&
                headerBuf[headerLen - 4] == '\r' && headerBuf[headerLen - 3] == '\n' &&
                headerBuf[headerLen - 2] == '\r' && headerBuf[headerLen - 1] == '\n') {
                headersComplete = true;
                break;
            }
        }
        headerBuf[headerLen] = '\0';

        if (!headersComplete) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: incomplete headers");
            return false;
        }

        // Parse status code from "HTTP/1.x NNN"
        int statusCode = 0;
        const char* statusStart = strchr(headerBuf, ' ');
        if (statusStart) {
            statusCode = atoi(statusStart + 1);
        }

        CLogger::Get()->Write(FromHttpClient, LogDebug, "DownloadFile: status %d", statusCode);

        // Handle redirects (301, 302, 303, 307, 308)
        if (statusCode >= 300 && statusCode < 400) {
            char redirectUrl[1024] = {0};
            if (!ParseLocationHeader(headerBuf, redirectUrl, sizeof(redirectUrl))) {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: redirect with no Location");
                return false;
            }

            CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: redirect -> %s", redirectUrl);
            return DownloadFileInternal(redirectUrl, sdPath, redirectsLeft - 1, deadlineTicks);
        }

        if (statusCode != 200) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: HTTP %d", statusCode);
            return false;
        }

        // Stream body to file
        FIL file;
        if (f_open(&file, sdPath, FA_WRITE | FA_CREATE_ALWAYS) != FR_OK) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: cannot create %s", sdPath);
            return false;
        }

        u8 buf[4096];
        unsigned totalWritten = 0;

        while (true) {
            if (IsDeadlineExpired(deadlineTicks)) {
                CLogger::Get()->Write(FromHttpClient, LogWarning,
                    "DownloadFile: deadline expired during body receive (%u bytes)", totalWritten);
                f_close(&file);
                return false;
            }

            int n = tlsSocket.Receive(buf, sizeof(buf), 0);
            if (n <= 0) break;

            UINT written;
            if (f_write(&file, buf, n, &written) != FR_OK || written != (UINT)n) {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: write failed at %u bytes", totalWritten);
                f_close(&file);
                return false;
            }
            totalWritten += written;

            // Sync and pet watchdog every ~64KB
            if ((totalWritten % 65536) < sizeof(buf)) {
                f_sync(&file);
                if (s_pWatchdog) {
                    s_pWatchdog->Start(15);
                }
            }
        }

        f_close(&file);
        CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: wrote %u bytes to %s", totalWritten, sdPath);
        return totalWritten > 0;

    } else {
        // Plain HTTP (non-TLS) path
        CSocket socket(m_pNet, IPPROTO_TCP);
        if (socket.Connect(ip, port) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: connect failed");
            return false;
        }

        if (IsDeadlineExpired(deadlineTicks)) {
            CLogger::Get()->Write(FromHttpClient, LogWarning, "DownloadFile: deadline expired after connect");
            return false;
        }

        if (socket.Send(request, strlen(request), 0) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: send failed");
            return false;
        }

        // Receive headers
        char headerBuf[4096];
        unsigned headerLen = 0;
        bool headersComplete = false;

        while (headerLen < sizeof(headerBuf) - 1) {
            if (IsDeadlineExpired(deadlineTicks)) {
                CLogger::Get()->Write(FromHttpClient, LogWarning,
                    "DownloadFile: deadline expired during header receive (%u bytes)", headerLen);
                return false;
            }

            int n = socket.Receive(&headerBuf[headerLen], 1, 0);
            if (n <= 0) break;
            headerLen++;

            if (headerLen >= 4 &&
                headerBuf[headerLen - 4] == '\r' && headerBuf[headerLen - 3] == '\n' &&
                headerBuf[headerLen - 2] == '\r' && headerBuf[headerLen - 1] == '\n') {
                headersComplete = true;
                break;
            }
        }
        headerBuf[headerLen] = '\0';

        if (!headersComplete) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: incomplete headers");
            return false;
        }

        int statusCode = 0;
        const char* statusStart = strchr(headerBuf, ' ');
        if (statusStart) {
            statusCode = atoi(statusStart + 1);
        }

        CLogger::Get()->Write(FromHttpClient, LogDebug, "DownloadFile: status %d", statusCode);

        if (statusCode >= 300 && statusCode < 400) {
            char redirectUrl[1024] = {0};
            if (!ParseLocationHeader(headerBuf, redirectUrl, sizeof(redirectUrl))) {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: redirect with no Location");
                return false;
            }

            CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: redirect -> %s", redirectUrl);
            return DownloadFileInternal(redirectUrl, sdPath, redirectsLeft - 1, deadlineTicks);
        }

        if (statusCode != 200) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: HTTP %d", statusCode);
            return false;
        }

        FIL file;
        if (f_open(&file, sdPath, FA_WRITE | FA_CREATE_ALWAYS) != FR_OK) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: cannot create %s", sdPath);
            return false;
        }

        u8 buf[4096];
        unsigned totalWritten = 0;

        while (true) {
            if (IsDeadlineExpired(deadlineTicks)) {
                CLogger::Get()->Write(FromHttpClient, LogWarning,
                    "DownloadFile: deadline expired during body receive (%u bytes)", totalWritten);
                f_close(&file);
                return false;
            }

            int n = socket.Receive(buf, sizeof(buf), 0);
            if (n <= 0) break;

            UINT written;
            if (f_write(&file, buf, n, &written) != FR_OK || written != (UINT)n) {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: write failed at %u bytes", totalWritten);
                f_close(&file);
                return false;
            }
            totalWritten += written;

            // Sync and pet watchdog every ~64KB
            if ((totalWritten % 65536) < sizeof(buf)) {
                f_sync(&file);
                if (s_pWatchdog) {
                    s_pWatchdog->Start(15);
                }
            }
        }

        f_close(&file);
        CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: wrote %u bytes to %s", totalWritten, sdPath);
        return totalWritten > 0;
    }
}

bool HttpClient::GetRaw(const char* url, HttpResponse* response, unsigned timeoutMs)
{
    response->success = false;
    response->statusCode = 0;
    response->bodyLength = 0;
    response->body[0] = '\0';

    if (s_pWatchdog) {
        s_pWatchdog->Start(15);
    }

    unsigned deadlineTicks = ComputeDeadline(timeoutMs);
    return GetRawInternal(url, response, 5, deadlineTicks);
}

bool HttpClient::GetRawInternal(const char* url, HttpResponse* response,
                                int redirectsLeft, unsigned deadlineTicks)
{
    if (redirectsLeft <= 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: too many redirects");
        return false;
    }

    if (!IsNetworkAvailable()) {
        CLogger::Get()->Write(FromHttpClient, LogWarning, "GetRaw: network unavailable, skipping");
        return false;
    }

    char host[256];
    char path[1024];
    bool useSSL = false;

    if (!ParseUrl(url, host, sizeof(host), path, sizeof(path), &useSSL)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: bad URL: %s", url);
        return false;
    }

    unsigned port = useSSL ? HTTPS_PORT : HTTP_PORT;

    // DNS resolve with timing
    unsigned totalStart = CTimer::GetClockTicks();
    unsigned phaseStart = totalStart;

    CIPAddress ip;
    CDNSClient dns(m_pNet);

    CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: resolving %s", host);
    if (!dns.Resolve(host, &ip)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: DNS failed for %s", host);
        return false;
    }

    unsigned dnsUs = CTimer::GetClockTicks() - phaseStart;
    CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: DNS resolved in %u us", dnsUs);

    if (IsDeadlineExpired(deadlineTicks)) {
        CLogger::Get()->Write(FromHttpClient, LogWarning, "GetRaw: deadline expired after DNS");
        return false;
    }

    // Build HTTP request
    char request[1536];
    snprintf(request, sizeof(request),
             "GET %s HTTP/1.0\r\n"
             "Host: %s\r\n"
             "User-Agent: MagicMirror/1.0\r\n"
             "Accept: */*\r\n"
             "Connection: close\r\n"
             "\r\n",
             path, host);

    phaseStart = CTimer::GetClockTicks();

    if (useSSL) {
        // HTTPS path — blocking TLS receives with deadline checks between iterations
        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: TLS connecting to %s:%u", host, port);

        CircleMbedTLS::CTLSSimpleClientSocket tlsSocket(m_pTLS, IPPROTO_TCP);
        if (tlsSocket.Setup(host) != 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: TLS setup failed");
            return false;
        }
        if (tlsSocket.Connect(ip, port) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: TLS connect failed");
            return false;
        }

        unsigned connectUs = CTimer::GetClockTicks() - phaseStart;
        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: TLS connected in %u us", connectUs);

        if (IsDeadlineExpired(deadlineTicks)) {
            CLogger::Get()->Write(FromHttpClient, LogWarning, "GetRaw: deadline expired after TLS connect");
            return false;
        }

        if (tlsSocket.Send(request, strlen(request), 0) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: TLS send failed");
            return false;
        }

        // Receive headers byte by byte (blocking, with deadline checks between iterations)
        phaseStart = CTimer::GetClockTicks();
        char headerBuf[4096];
        unsigned headerLen = 0;
        bool headersComplete = false;

        while (headerLen < sizeof(headerBuf) - 1) {
            if (IsDeadlineExpired(deadlineTicks)) {
                CLogger::Get()->Write(FromHttpClient, LogWarning,
                    "GetRaw: deadline expired during header receive (%u bytes)", headerLen);
                return false;
            }

            int n = tlsSocket.Receive((u8*)&headerBuf[headerLen], 1, 0);
            if (n <= 0) break;
            headerLen++;

            if (headerLen >= 4 &&
                headerBuf[headerLen - 4] == '\r' && headerBuf[headerLen - 3] == '\n' &&
                headerBuf[headerLen - 2] == '\r' && headerBuf[headerLen - 1] == '\n') {
                headersComplete = true;
                break;
            }
        }
        headerBuf[headerLen] = '\0';

        unsigned headerUs = CTimer::GetClockTicks() - phaseStart;
        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: headers received in %u us (%u bytes)",
                              headerUs, headerLen);

        if (!headersComplete) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: incomplete headers");
            return false;
        }

        // Parse status code from "HTTP/1.x NNN"
        int statusCode = 0;
        const char* statusStart = strchr(headerBuf, ' ');
        if (statusStart) {
            statusCode = atoi(statusStart + 1);
        }

        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: status %d", statusCode);

        // Handle redirects (301, 302, 303, 307, 308)
        if (statusCode >= 300 && statusCode < 400) {
            char redirectUrl[1024] = {0};
            if (!ParseLocationHeader(headerBuf, redirectUrl, sizeof(redirectUrl))) {
                CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: redirect with no Location");
                return false;
            }

            CLogger::Get()->Write(FromHttpClient, LogNotice, "GetRaw: redirect -> %s", redirectUrl);
            return GetRawInternal(redirectUrl, response, redirectsLeft - 1, deadlineTicks);
        }

        response->statusCode = statusCode;

        if (statusCode != 200) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: HTTP %d", statusCode);
            return false;
        }

        // Read body into response buffer
        phaseStart = CTimer::GetClockTicks();
        unsigned maxBody = sizeof(response->body) - 1;
        unsigned totalRead = 0;
        u8 buf[4096];

        while (totalRead < maxBody) {
            if (IsDeadlineExpired(deadlineTicks)) {
                CLogger::Get()->Write(FromHttpClient, LogWarning,
                    "GetRaw: deadline expired during body receive (%u bytes)", totalRead);
                return false;
            }

            unsigned toRead = sizeof(buf);
            if (totalRead + toRead > maxBody) {
                toRead = maxBody - totalRead;
            }
            int n = tlsSocket.Receive(buf, toRead, 0);
            if (n <= 0) break;

            memcpy(response->body + totalRead, buf, n);
            totalRead += n;
        }

        unsigned bodyUs = CTimer::GetClockTicks() - phaseStart;
        unsigned totalUs = CTimer::GetClockTicks() - totalStart;

        response->body[totalRead] = '\0';
        response->bodyLength = totalRead;
        response->success = true;

        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: body %u bytes in %u us (total %u us)",
                              totalRead, bodyUs, totalUs);

        return true;

    } else {
        // Plain HTTP path — non-blocking receives via ReceiveWithTimeout
        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: connecting to %s:%u", host, port);

        CSocket socket(m_pNet, IPPROTO_TCP);
        if (socket.Connect(ip, port) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: connect failed");
            return false;
        }

        unsigned connectUs = CTimer::GetClockTicks() - phaseStart;
        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: connected in %u us", connectUs);

        if (IsDeadlineExpired(deadlineTicks)) {
            CLogger::Get()->Write(FromHttpClient, LogWarning, "GetRaw: deadline expired after connect");
            return false;
        }

        if (socket.Send(request, strlen(request), 0) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: send failed");
            return false;
        }

        // Receive headers byte by byte (non-blocking with timeout)
        phaseStart = CTimer::GetClockTicks();
        char headerBuf[4096];
        unsigned headerLen = 0;
        bool headersComplete = false;

        while (headerLen < sizeof(headerBuf) - 1) {
            int n = ReceiveWithTimeout(socket, &headerBuf[headerLen], 1, deadlineTicks);
            if (n <= 0) {
                if (n == 0) {
                    CLogger::Get()->Write(FromHttpClient, LogWarning,
                        "GetRaw: deadline expired during header receive (%u bytes)", headerLen);
                    return false;
                }
                break;  // connection closed or error
            }
            headerLen++;

            if (headerLen >= 4 &&
                headerBuf[headerLen - 4] == '\r' && headerBuf[headerLen - 3] == '\n' &&
                headerBuf[headerLen - 2] == '\r' && headerBuf[headerLen - 1] == '\n') {
                headersComplete = true;
                break;
            }
        }
        headerBuf[headerLen] = '\0';

        unsigned headerUs = CTimer::GetClockTicks() - phaseStart;
        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: headers received in %u us (%u bytes)",
                              headerUs, headerLen);

        if (!headersComplete) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: incomplete headers");
            return false;
        }

        // Parse status code
        int statusCode = 0;
        const char* statusStart = strchr(headerBuf, ' ');
        if (statusStart) {
            statusCode = atoi(statusStart + 1);
        }

        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: status %d", statusCode);

        // Handle redirects
        if (statusCode >= 300 && statusCode < 400) {
            char redirectUrl[1024] = {0};
            if (!ParseLocationHeader(headerBuf, redirectUrl, sizeof(redirectUrl))) {
                CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: redirect with no Location");
                return false;
            }

            CLogger::Get()->Write(FromHttpClient, LogNotice, "GetRaw: redirect -> %s", redirectUrl);
            return GetRawInternal(redirectUrl, response, redirectsLeft - 1, deadlineTicks);
        }

        response->statusCode = statusCode;

        if (statusCode != 200) {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: HTTP %d", statusCode);
            return false;
        }

        // Read body (non-blocking)
        phaseStart = CTimer::GetClockTicks();
        unsigned maxBody = sizeof(response->body) - 1;
        unsigned totalRead = 0;
        u8 buf[4096];

        while (totalRead < maxBody) {
            unsigned toRead = sizeof(buf);
            if (totalRead + toRead > maxBody) {
                toRead = maxBody - totalRead;
            }

            int n = ReceiveWithTimeout(socket, buf, toRead, deadlineTicks);
            if (n <= 0) {
                if (n == 0) {
                    CLogger::Get()->Write(FromHttpClient, LogWarning,
                        "GetRaw: deadline expired during body receive (%u bytes)", totalRead);
                    return false;
                }
                break;  // connection closed or error
            }

            memcpy(response->body + totalRead, buf, n);
            totalRead += n;
        }

        unsigned bodyUs = CTimer::GetClockTicks() - phaseStart;
        unsigned totalUs = CTimer::GetClockTicks() - totalStart;

        response->body[totalRead] = '\0';
        response->bodyLength = totalRead;
        response->success = true;

        CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: body %u bytes in %u us (total %u us)",
                              totalRead, bodyUs, totalUs);

        return true;
    }
}

} // namespace mm
