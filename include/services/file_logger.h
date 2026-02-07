#ifndef FILE_LOGGER_H
#define FILE_LOGGER_H

#include <circle/logger.h>
#include <circle/time.h>
#include <fatfs/ff.h>

namespace mm {

class FileLogger
{
public:
    FileLogger(unsigned nMaxLines = 1000);
    ~FileLogger();

    bool Initialize();
    void Close();

    // Call periodically from main loop to flush pending log events to file
    void Update();

private:
    static const char* SeverityToString(TLogSeverity severity);

    FIL m_File;
    bool m_bFileOpen;
    unsigned m_nLineCount;
    unsigned m_nMaxLines;
};

} // namespace mm

#endif // FILE_LOGGER_H
