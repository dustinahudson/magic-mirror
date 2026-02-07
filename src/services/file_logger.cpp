#include "services/file_logger.h"
#include <string.h>
#include <stdio.h>

static const char LOG_FILE[] = "SD:/mm.log";

namespace mm {

FileLogger::FileLogger(unsigned nMaxLines)
    : m_bFileOpen(false),
      m_nLineCount(0),
      m_nMaxLines(nMaxLines)
{
}

FileLogger::~FileLogger()
{
    Close();
}

bool FileLogger::Initialize()
{
    if (f_open(&m_File, LOG_FILE, FA_WRITE | FA_CREATE_ALWAYS) != FR_OK) {
        return false;
    }

    m_bFileOpen = true;
    m_nLineCount = 0;
    return true;
}

void FileLogger::Close()
{
    if (m_bFileOpen) {
        f_sync(&m_File);
        f_close(&m_File);
        m_bFileOpen = false;
    }
}

void FileLogger::Update()
{
    if (!m_bFileOpen) {
        return;
    }

    TLogSeverity severity;
    char source[LOG_MAX_SOURCE];
    char message[LOG_MAX_MESSAGE];
    time_t time;
    unsigned hundredthTime;
    int timeZone;

    bool flushed = false;

    while (CLogger::Get()->ReadEvent(&severity, source, message,
                                      &time, &hundredthTime, &timeZone)) {
        // Format timestamp from epoch time
        CTime t;
        t.Set(time);

        char line[512];
        snprintf(line, sizeof(line), "%02u:%02u:%02u.%02u %-7s %s: %s\n",
                 t.GetHours(), t.GetMinutes(), t.GetSeconds(), hundredthTime,
                 SeverityToString(severity), source, message);

        UINT written;
        UINT len = strlen(line);
        if (f_write(&m_File, line, len, &written) == FR_OK && written == len) {
            m_nLineCount++;
            flushed = true;
        }

        if (m_nLineCount >= m_nMaxLines) {
            // Truncate and start over
            f_lseek(&m_File, 0);
            f_truncate(&m_File);
            m_nLineCount = 0;
        }
    }

    if (flushed) {
        f_sync(&m_File);
    }
}

const char* FileLogger::SeverityToString(TLogSeverity severity)
{
    switch (severity) {
        case LogPanic:   return "PANIC";
        case LogError:   return "ERROR";
        case LogWarning: return "WARN";
        case LogNotice:  return "NOTICE";
        case LogDebug:   return "DEBUG";
        default:         return "?";
    }
}

} // namespace mm
