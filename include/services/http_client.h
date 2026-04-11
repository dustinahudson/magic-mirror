#ifndef HTTP_CLIENT_H
#define HTTP_CLIENT_H

#include <circle/net/netsubsystem.h>
#include <circle/net/socket.h>
#include <circle/string.h>
#include <circle/types.h>
#include <circle/bcmwatchdog.h>
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
    // Timeout constants
    static const unsigned HTTP_TIMEOUT_MS = 12000;      // API calls, calendar fetches
    static const unsigned DOWNLOAD_TIMEOUT_MS = 120000; // OTA firmware downloads

    HttpClient(CNetSubSystem* pNet, CircleMbedTLS::CTLSSimpleSupport* pTLS);
    ~HttpClient();

    // Set watchdog for petting during long downloads
    static void SetWatchdog(CBcmWatchdog* pWatchdog);

    // Fetch a URL (delegates to GetRaw)
    bool Get(const char* url, HttpResponse* response);

    // Fetch with explicit host/path (delegates to GetRaw)
    bool Get(const char* host, const char* path, bool useSSL, HttpResponse* response);

    // Download a file from URL to SD card path, following redirects
    bool DownloadFile(const char* url, const char* sdPath,
                      unsigned timeoutMs = DOWNLOAD_TIMEOUT_MS);

    // Raw-socket GET with redirect handling and timeout
    bool GetRaw(const char* url, HttpResponse* response,
                unsigned timeoutMs = HTTP_TIMEOUT_MS);

    // Shared response buffer - single 512KB buffer for all services (single-threaded)
    static HttpResponse* GetSharedResponse();

private:
    bool ParseUrl(const char* url, char* host, size_t hostLen,
                  char* path, size_t pathLen, bool* useSSL);

    // Internal helpers with redirect depth and deadline tracking
    bool DownloadFileInternal(const char* url, const char* sdPath,
                              int redirectsLeft, unsigned deadlineTicks);
    bool GetRawInternal(const char* url, HttpResponse* response,
                        int redirectsLeft, unsigned deadlineTicks);

    // Deadline infrastructure
    static unsigned ComputeDeadline(unsigned timeoutMs);
    static bool IsDeadlineExpired(unsigned deadlineTicks);

    // Non-blocking receive for plain TCP sockets
    static int ReceiveWithTimeout(CSocket& socket, void* buf, unsigned size,
                                  unsigned deadlineTicks);

    CNetSubSystem* m_pNet;
    CircleMbedTLS::CTLSSimpleSupport* m_pTLS;
    static CBcmWatchdog* s_pWatchdog;
};

} // namespace mm

#endif // HTTP_CLIENT_H
