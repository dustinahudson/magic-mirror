#ifndef HTTP_CLIENT_H
#define HTTP_CLIENT_H

#include <circle/net/netsubsystem.h>
#include <circle/net/socket.h>
#include <circle/string.h>
#include <circle/types.h>
#include <circle-mbedtls/tlssimplesupport.h>
#include <circle-mbedtls/tlssimpleclientsocket.h>

namespace mm {

// HTTP response structure
struct HttpResponse {
    int statusCode;
    char body[524288];      // Response body (512KB for large ICS files)
    unsigned bodyLength;
    bool success;
};

class HttpClient
{
public:
    HttpClient(CNetSubSystem* pNet, CircleMbedTLS::CTLSSimpleSupport* pTLS);
    ~HttpClient();

    // Fetch a URL (supports both HTTP and HTTPS)
    bool Get(const char* url, HttpResponse* response);

    // Fetch with explicit host/path (for when you've already parsed the URL)
    bool Get(const char* host, const char* path, bool useSSL, HttpResponse* response);

    // Download a file from URL to SD card path, following redirects
    bool DownloadFile(const char* url, const char* sdPath);

    // Raw-socket GET with redirect handling (avoids Circle's CHTTPClient)
    bool GetRaw(const char* url, HttpResponse* response);

private:
    bool ParseUrl(const char* url, char* host, size_t hostLen,
                  char* path, size_t pathLen, bool* useSSL);

    // Internal download helper with redirect depth tracking
    bool DownloadFileInternal(const char* url, const char* sdPath, int redirectsLeft);

    // Internal raw GET helper with redirect depth tracking
    bool GetRawInternal(const char* url, HttpResponse* response, int redirectsLeft);

    CNetSubSystem* m_pNet;
    CircleMbedTLS::CTLSSimpleSupport* m_pTLS;
};

} // namespace mm

#endif // HTTP_CLIENT_H
