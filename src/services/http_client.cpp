#include "services/http_client.h"
#include <circle/net/dnsclient.h>
#include <circle/net/in.h>
#include <circle/logger.h>
#include <circle-mbedtls/httpclient.h>
#include <fatfs/ff.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>

static const char FromHttpClient[] = "http";

namespace mm {

HttpClient::HttpClient(CNetSubSystem* pNet, CircleMbedTLS::CTLSSimpleSupport* pTLS)
    : m_pNet(pNet),
      m_pTLS(pTLS)
{
}

HttpClient::~HttpClient()
{
}

bool HttpClient::Get(const char* url, HttpResponse* response)
{
    char host[256];
    char path[512];
    bool useSSL = false;

    if (!ParseUrl(url, host, sizeof(host), path, sizeof(path), &useSSL)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "Failed to parse URL: %s", url);
        response->success = false;
        return false;
    }

    return Get(host, path, useSSL, response);
}

bool HttpClient::Get(const char* host, const char* path, bool useSSL, HttpResponse* response)
{
    response->success = false;
    response->statusCode = 0;
    response->bodyLength = 0;
    response->body[0] = '\0';

    // Resolve hostname
    CIPAddress ip;
    CDNSClient dns(m_pNet);

    CLogger::Get()->Write(FromHttpClient, LogDebug, "Resolving: %s", host);
    if (!dns.Resolve(host, &ip)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "DNS failed for: %s", host);
        return false;
    }

    CString ipStr;
    ip.Format(&ipStr);
    CLogger::Get()->Write(FromHttpClient, LogDebug, "Resolved %s to %s", host, (const char*)ipStr);

    // Create HTTPS client
    unsigned port = useSSL ? HTTPS_PORT : HTTP_PORT;

    CircleMbedTLS::CHTTPClient client(m_pTLS, ip, port, host, useSSL);

    // Perform GET request
    unsigned bufferSize = sizeof(response->body) - 1;

    CLogger::Get()->Write(FromHttpClient, LogDebug, "GET %s%s", useSSL ? "https://" : "http://", host);

    CircleMbedTLS::THTTPStatus status = client.Get(path, (u8*)response->body, &bufferSize);

    if (status != CircleMbedTLS::HTTPOK) {
        CLogger::Get()->Write(FromHttpClient, LogError, "HTTP GET failed: status=%d", status);
        response->statusCode = (int)status;
        return false;
    }

    response->body[bufferSize] = '\0';
    response->bodyLength = bufferSize;
    response->statusCode = 200;
    response->success = true;

    CLogger::Get()->Write(FromHttpClient, LogDebug, "Received %u bytes", bufferSize);

    return true;
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

bool HttpClient::DownloadFile(const char* url, const char* sdPath)
{
    return DownloadFileInternal(url, sdPath, 5);
}

bool HttpClient::DownloadFileInternal(const char* url, const char* sdPath, int redirectsLeft)
{
    if (redirectsLeft <= 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "Too many redirects");
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

        if (tlsSocket.Send(request, strlen(request), 0) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: TLS send failed");
            return false;
        }

        // Receive headers first - read byte by byte to find end of headers
        char headerBuf[4096];
        unsigned headerLen = 0;
        bool headersComplete = false;

        while (headerLen < sizeof(headerBuf) - 1) {
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
            // Find Location header (case-insensitive search)
            char redirectUrl[1024] = {0};
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
                        if (len < sizeof(redirectUrl)) {
                            strncpy(redirectUrl, p, len);
                            redirectUrl[len] = '\0';
                        }
                    }
                    break;
                }
                // Skip to next line
                const char* nl = strchr(p, '\n');
                if (!nl) break;
                p = nl + 1;
            }

            if (redirectUrl[0] == '\0') {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: redirect with no Location");
                return false;
            }

            CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: redirect -> %s", redirectUrl);
            return DownloadFileInternal(redirectUrl, sdPath, redirectsLeft - 1);
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
            int n = tlsSocket.Receive(buf, sizeof(buf), 0);
            if (n <= 0) break;

            UINT written;
            if (f_write(&file, buf, n, &written) != FR_OK || written != (UINT)n) {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: write failed at %u bytes", totalWritten);
                f_close(&file);
                return false;
            }
            totalWritten += written;

            // Sync periodically (every ~64KB)
            if ((totalWritten % 65536) < sizeof(buf)) {
                f_sync(&file);
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

        if (socket.Send(request, strlen(request), 0) < 0) {
            CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: send failed");
            return false;
        }

        // Receive headers
        char headerBuf[4096];
        unsigned headerLen = 0;
        bool headersComplete = false;

        while (headerLen < sizeof(headerBuf) - 1) {
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
                        if (len < sizeof(redirectUrl)) {
                            strncpy(redirectUrl, p, len);
                            redirectUrl[len] = '\0';
                        }
                    }
                    break;
                }
                const char* nl = strchr(p, '\n');
                if (!nl) break;
                p = nl + 1;
            }

            if (redirectUrl[0] == '\0') {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: redirect with no Location");
                return false;
            }

            CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: redirect -> %s", redirectUrl);
            return DownloadFileInternal(redirectUrl, sdPath, redirectsLeft - 1);
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
            int n = socket.Receive(buf, sizeof(buf), 0);
            if (n <= 0) break;

            UINT written;
            if (f_write(&file, buf, n, &written) != FR_OK || written != (UINT)n) {
                CLogger::Get()->Write(FromHttpClient, LogError, "DownloadFile: write failed at %u bytes", totalWritten);
                f_close(&file);
                return false;
            }
            totalWritten += written;

            if ((totalWritten % 65536) < sizeof(buf)) {
                f_sync(&file);
            }
        }

        f_close(&file);
        CLogger::Get()->Write(FromHttpClient, LogNotice, "DownloadFile: wrote %u bytes to %s", totalWritten, sdPath);
        return totalWritten > 0;
    }
}

bool HttpClient::GetRaw(const char* url, HttpResponse* response)
{
    response->success = false;
    response->statusCode = 0;
    response->bodyLength = 0;
    response->body[0] = '\0';

    return GetRawInternal(url, response, 5);
}

bool HttpClient::GetRawInternal(const char* url, HttpResponse* response, int redirectsLeft)
{
    if (redirectsLeft <= 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: too many redirects");
        return false;
    }

    char host[256];
    char path[1024];
    bool useSSL = false;

    if (!ParseUrl(url, host, sizeof(host), path, sizeof(path), &useSSL)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: bad URL: %s", url);
        return false;
    }

    if (!useSSL) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: only HTTPS is supported");
        return false;
    }

    unsigned port = HTTPS_PORT;

    // DNS resolve
    CIPAddress ip;
    CDNSClient dns(m_pNet);

    CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: resolving %s", host);
    if (!dns.Resolve(host, &ip)) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: DNS failed for %s", host);
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

    CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: connecting to %s:%u", host, port);

    CircleMbedTLS::CTLSSimpleClientSocket tlsSocket(m_pTLS, IPPROTO_TCP);
    if (tlsSocket.Setup(host) != 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: TLS setup failed");
        return false;
    }
    if (tlsSocket.Connect(ip, port) < 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: TLS connect failed");
        return false;
    }

    if (tlsSocket.Send(request, strlen(request), 0) < 0) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: TLS send failed");
        return false;
    }

    // Receive headers byte by byte to find end of headers
    char headerBuf[4096];
    unsigned headerLen = 0;
    bool headersComplete = false;

    while (headerLen < sizeof(headerBuf) - 1) {
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
                    if (len < sizeof(redirectUrl)) {
                        strncpy(redirectUrl, p, len);
                        redirectUrl[len] = '\0';
                    }
                }
                break;
            }
            const char* nl = strchr(p, '\n');
            if (!nl) break;
            p = nl + 1;
        }

        if (redirectUrl[0] == '\0') {
            CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: redirect with no Location");
            return false;
        }

        CLogger::Get()->Write(FromHttpClient, LogNotice, "GetRaw: redirect -> %s", redirectUrl);
        return GetRawInternal(redirectUrl, response, redirectsLeft - 1);
    }

    response->statusCode = statusCode;

    if (statusCode != 200) {
        CLogger::Get()->Write(FromHttpClient, LogError, "GetRaw: HTTP %d", statusCode);
        return false;
    }

    // Read body into response buffer
    unsigned maxBody = sizeof(response->body) - 1;
    unsigned totalRead = 0;
    u8 buf[4096];

    while (totalRead < maxBody) {
        unsigned toRead = sizeof(buf);
        if (totalRead + toRead > maxBody) {
            toRead = maxBody - totalRead;
        }
        int n = tlsSocket.Receive(buf, toRead, 0);
        if (n <= 0) break;

        memcpy(response->body + totalRead, buf, n);
        totalRead += n;
    }

    response->body[totalRead] = '\0';
    response->bodyLength = totalRead;
    response->success = true;

    CLogger::Get()->Write(FromHttpClient, LogDebug, "GetRaw: received %u bytes", totalRead);

    return true;
}

} // namespace mm
