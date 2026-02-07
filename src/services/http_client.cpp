#include "services/http_client.h"
#include <circle/net/dnsclient.h>
#include <circle/logger.h>
#include <circle-mbedtls/httpclient.h>
#include <string.h>

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

} // namespace mm
