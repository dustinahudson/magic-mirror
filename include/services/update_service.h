#ifndef UPDATE_SERVICE_H
#define UPDATE_SERVICE_H

#include "services/http_client.h"

namespace mm {

class UpdateService
{
public:
    UpdateService(HttpClient* pHttpClient);

    // Check GitHub releases API, compare to current version.
    // Returns true if update was downloaded and installed (caller should reboot).
    bool CheckAndUpdate();

private:
    // GET /repos/dustinahudson/magic-mirror/releases/latest
    // Parses tag_name and first asset's browser_download_url
    bool FetchLatestRelease(char* outTag, size_t tagLen,
                            char* outAssetUrl, size_t urlLen);

    // Read current version from SD:/version.txt
    bool ReadCurrentVersion(char* outVersion, size_t maxLen);

    // Write new version to SD:/version.txt
    bool WriteVersion(const char* version);

    // Returns true if remoteTag differs from localTag (any difference = update)
    bool IsNewer(const char* remoteTag, const char* localTag);

    // Download kernel.img to SD, swap files
    bool DownloadAndInstall(const char* assetUrl, const char* newVersion);

    HttpClient* m_pHttpClient;
};

} // namespace mm

#endif // UPDATE_SERVICE_H
