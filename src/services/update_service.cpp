#include "services/update_service.h"
#include <circle/logger.h>
#include <fatfs/ff.h>
#include <string.h>
#include <stdlib.h>

static const char FromUpdate[] = "update";

static const char GITHUB_API_HOST[] = "api.github.com";
static const char RELEASES_PATH[] = "/repos/dustinahudson/magic-mirror/releases/latest";
static const char VERSION_FILE[] = "SD:/version.txt";
static const char KERNEL_NEW[] = "SD:/kernel.new";
static const char KERNEL_IMG[] = "SD:/kernel.img";

namespace mm {

UpdateService::UpdateService(HttpClient* pHttpClient)
    : m_pHttpClient(pHttpClient)
{
}

bool UpdateService::CheckAndUpdate()
{
    if (!m_pHttpClient) {
        return false;
    }

    // Read current version
    char currentVersion[64] = {0};
    if (!ReadCurrentVersion(currentVersion, sizeof(currentVersion))) {
        // No version.txt yet — use compile-time version
#ifdef APP_VERSION
        strncpy(currentVersion, APP_VERSION, sizeof(currentVersion) - 1);
        CLogger::Get()->Write(FromUpdate, LogNotice,
                              "No version.txt, using compile-time version: %s", currentVersion);
#else
        CLogger::Get()->Write(FromUpdate, LogWarning, "No version available, skipping update check");
        return false;
#endif
    } else {
        CLogger::Get()->Write(FromUpdate, LogNotice, "Current version: %s", currentVersion);
    }

    // Fetch latest release info from GitHub
    char remoteTag[64] = {0};
    char assetUrl[512] = {0};

    if (!FetchLatestRelease(remoteTag, sizeof(remoteTag), assetUrl, sizeof(assetUrl))) {
        CLogger::Get()->Write(FromUpdate, LogWarning, "Failed to fetch latest release info");
        return false;
    }

    CLogger::Get()->Write(FromUpdate, LogNotice, "Latest release: %s", remoteTag);

    // Compare versions
    if (!IsNewer(remoteTag, currentVersion)) {
        CLogger::Get()->Write(FromUpdate, LogNotice, "Already up to date");
        return false;
    }

    CLogger::Get()->Write(FromUpdate, LogNotice, "Update available: %s -> %s",
                          currentVersion, remoteTag);

    // Download and install
    if (!DownloadAndInstall(assetUrl, remoteTag)) {
        CLogger::Get()->Write(FromUpdate, LogError, "Update failed");
        return false;
    }

    CLogger::Get()->Write(FromUpdate, LogNotice, "Update installed successfully: %s", remoteTag);
    return true;
}

bool UpdateService::FetchLatestRelease(char* outTag, size_t tagLen,
                                       char* outAssetUrl, size_t urlLen)
{
    HttpResponse response;
    if (!m_pHttpClient->Get(GITHUB_API_HOST, RELEASES_PATH, true, &response)) {
        CLogger::Get()->Write(FromUpdate, LogError, "GitHub API request failed");
        return false;
    }

    // Parse tag_name from JSON response
    // Looking for: "tag_name": "v1.0.0"
    const char* tagKey = strstr(response.body, "\"tag_name\"");
    if (!tagKey) {
        CLogger::Get()->Write(FromUpdate, LogError, "No tag_name in response");
        return false;
    }

    // Skip to the value
    const char* p = tagKey + 10; // skip "tag_name"
    while (*p && *p != '"') p++;
    if (*p != '"') return false;
    p++; // skip opening quote

    size_t i = 0;
    while (*p && *p != '"' && i < tagLen - 1) {
        outTag[i++] = *p++;
    }
    outTag[i] = '\0';

    // Parse first asset's browser_download_url
    // Looking for: "browser_download_url": "https://..."
    const char* urlKey = strstr(response.body, "\"browser_download_url\"");
    if (!urlKey) {
        CLogger::Get()->Write(FromUpdate, LogError, "No browser_download_url in response");
        return false;
    }

    p = urlKey + 22; // skip "browser_download_url"
    while (*p && *p != '"') p++;
    if (*p != '"') return false;
    p++; // skip opening quote

    i = 0;
    while (*p && *p != '"' && i < urlLen - 1) {
        outAssetUrl[i++] = *p++;
    }
    outAssetUrl[i] = '\0';

    CLogger::Get()->Write(FromUpdate, LogDebug, "Release tag: %s", outTag);
    CLogger::Get()->Write(FromUpdate, LogDebug, "Asset URL: %s", outAssetUrl);

    return outTag[0] != '\0' && outAssetUrl[0] != '\0';
}

bool UpdateService::ReadCurrentVersion(char* outVersion, size_t maxLen)
{
    FIL file;
    if (f_open(&file, VERSION_FILE, FA_READ) != FR_OK) {
        return false;
    }

    UINT bytesRead;
    if (f_read(&file, outVersion, maxLen - 1, &bytesRead) != FR_OK) {
        f_close(&file);
        return false;
    }

    outVersion[bytesRead] = '\0';
    f_close(&file);

    // Trim trailing whitespace/newlines
    while (bytesRead > 0 && (outVersion[bytesRead - 1] == '\n' ||
           outVersion[bytesRead - 1] == '\r' ||
           outVersion[bytesRead - 1] == ' ')) {
        outVersion[--bytesRead] = '\0';
    }

    return bytesRead > 0;
}

bool UpdateService::WriteVersion(const char* version)
{
    FIL file;
    if (f_open(&file, VERSION_FILE, FA_WRITE | FA_CREATE_ALWAYS) != FR_OK) {
        CLogger::Get()->Write(FromUpdate, LogError, "Cannot create %s", VERSION_FILE);
        return false;
    }

    UINT written;
    UINT len = strlen(version);
    if (f_write(&file, version, len, &written) != FR_OK || written != len) {
        CLogger::Get()->Write(FromUpdate, LogError, "Failed to write version");
        f_close(&file);
        return false;
    }

    f_close(&file);
    return true;
}

bool UpdateService::IsNewer(const char* remoteTag, const char* localTag)
{
    return strcmp(remoteTag, localTag) != 0;
}

bool UpdateService::DownloadAndInstall(const char* assetUrl, const char* newVersion)
{
    CLogger::Get()->Write(FromUpdate, LogNotice, "Downloading update from: %s", assetUrl);

    // Download to temporary file
    if (!m_pHttpClient->DownloadFile(assetUrl, KERNEL_NEW)) {
        CLogger::Get()->Write(FromUpdate, LogError, "Download failed");
        f_unlink(KERNEL_NEW);
        return false;
    }

    // Verify file exists and has content
    FILINFO finfo;
    if (f_stat(KERNEL_NEW, &finfo) != FR_OK || finfo.fsize == 0) {
        CLogger::Get()->Write(FromUpdate, LogError, "Downloaded file is empty or missing");
        f_unlink(KERNEL_NEW);
        return false;
    }

    CLogger::Get()->Write(FromUpdate, LogNotice, "Downloaded %lu bytes, installing...",
                          (unsigned long)finfo.fsize);

    // Swap: delete old kernel, rename new one
    f_unlink(KERNEL_IMG);

    if (f_rename(KERNEL_NEW, KERNEL_IMG) != FR_OK) {
        CLogger::Get()->Write(FromUpdate, LogError, "Failed to rename kernel.new -> kernel.img");
        return false;
    }

    // Write new version tag
    if (!WriteVersion(newVersion)) {
        CLogger::Get()->Write(FromUpdate, LogWarning, "Failed to write version file (update still installed)");
    }

    return true;
}

} // namespace mm
