#include "modules/widgets/datetime_widget.h"
#include <circle/util.h>

namespace mm {

DateTimeWidget::DateTimeWidget(lv_obj_t* parent, CTimer* timer)
    : WidgetBase("DateTime", parent, timer),
      m_pDateLabel(nullptr),
      m_pTimeLabel(nullptr),
      m_pSecondsLabel(nullptr),
      m_pAmPmLabel(nullptr),
      m_timezoneOffset(0),
      m_lastUpdateTime(0)
{
    m_dateBuffer[0] = '\0';
    m_timeBuffer[0] = '\0';
    m_secondsBuffer[0] = '\0';
    m_ampmBuffer[0] = '\0';
}

DateTimeWidget::~DateTimeWidget()
{
    // LVGL objects are children of m_pContainer and will be deleted with it
}

bool DateTimeWidget::Initialize()
{
    CreateUI();
    Update();
    return true;
}

void DateTimeWidget::CreateUI()
{
    // Date label (smaller, at top)
    m_pDateLabel = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(m_pDateLabel, lv_color_make(180, 180, 180), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pDateLabel, &lv_font_montserrat_24, LV_PART_MAIN);
    lv_obj_align(m_pDateLabel, LV_ALIGN_TOP_LEFT, 0, 0);
    lv_label_set_text(m_pDateLabel, "");

    // Time label (large)
    m_pTimeLabel = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(m_pTimeLabel, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pTimeLabel, &lv_font_montserrat_48, LV_PART_MAIN);
    lv_obj_align(m_pTimeLabel, LV_ALIGN_TOP_LEFT, 0, 34);
    lv_label_set_text(m_pTimeLabel, "");

    // Seconds label (smaller, next to time)
    m_pSecondsLabel = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(m_pSecondsLabel, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pSecondsLabel, &lv_font_montserrat_24, LV_PART_MAIN);
    lv_obj_align_to(m_pSecondsLabel, m_pTimeLabel, LV_ALIGN_OUT_RIGHT_TOP, 5, 0);
    lv_label_set_text(m_pSecondsLabel, "");

    // AM/PM label
    m_pAmPmLabel = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(m_pAmPmLabel, lv_color_make(180, 180, 180), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pAmPmLabel, &lv_font_montserrat_24, LV_PART_MAIN);
    lv_obj_align_to(m_pAmPmLabel, m_pTimeLabel, LV_ALIGN_OUT_RIGHT_BOTTOM, 5, 0);
    lv_label_set_text(m_pAmPmLabel, "");
}

void DateTimeWidget::Update()
{
    unsigned currentTime = m_pTimer->GetTime();

    // Update every second
    if (currentTime != m_lastUpdateTime) {
        UpdateTime();
        UpdateDate();
        m_lastUpdateTime = currentTime;

        // Reposition seconds and am/pm after time text changes
        lv_obj_align_to(m_pSecondsLabel, m_pTimeLabel, LV_ALIGN_OUT_RIGHT_TOP, 5, 0);
        lv_obj_align_to(m_pAmPmLabel, m_pTimeLabel, LV_ALIGN_OUT_RIGHT_BOTTOM, 5, 0);
    }
}

void DateTimeWidget::UpdateTime()
{
    unsigned unixTime = m_pTimer->GetTime() + m_timezoneOffset;

    // Calculate time components from Unix timestamp
    unsigned secondsInDay = unixTime % 86400;
    unsigned hours = secondsInDay / 3600;
    unsigned minutes = (secondsInDay % 3600) / 60;
    unsigned seconds = secondsInDay % 60;

    bool isPM = hours >= 12;

    // Convert to 12-hour format
    int hour12 = hours;
    if (hour12 == 0) {
        hour12 = 12;
    } else if (hour12 > 12) {
        hour12 -= 12;
    }

    // Format time string
    CString timeStr;
    timeStr.Format("%d:%02d", hour12, minutes);
    strncpy(m_timeBuffer, (const char*)timeStr, sizeof(m_timeBuffer) - 1);
    m_timeBuffer[sizeof(m_timeBuffer) - 1] = '\0';

    // Format seconds
    CString secStr;
    secStr.Format("%02d", seconds);
    strncpy(m_secondsBuffer, (const char*)secStr, sizeof(m_secondsBuffer) - 1);
    m_secondsBuffer[sizeof(m_secondsBuffer) - 1] = '\0';

    // AM/PM
    strncpy(m_ampmBuffer, isPM ? "pm" : "am", sizeof(m_ampmBuffer) - 1);
    m_ampmBuffer[sizeof(m_ampmBuffer) - 1] = '\0';

    // Update labels
    lv_label_set_text(m_pTimeLabel, m_timeBuffer);
    lv_label_set_text(m_pSecondsLabel, m_secondsBuffer);
    lv_label_set_text(m_pAmPmLabel, m_ampmBuffer);
}

void DateTimeWidget::UpdateDate()
{
    unsigned unixTime = m_pTimer->GetTime() + m_timezoneOffset;

    // Calculate date from Unix timestamp (days since 1970-01-01)
    unsigned days = unixTime / 86400;

    // Calculate day of week (1970-01-01 was Thursday = 4)
    unsigned dayOfWeek = (days + 4) % 7;

    // Calculate year, month, day using algorithm
    unsigned year = 1970;
    unsigned month = 0;
    unsigned dayOfMonth = 0;

    // Days in each month (non-leap year)
    static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};

    unsigned remainingDays = days;

    // Find year
    while (true) {
        bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
        unsigned daysInYear = isLeap ? 366 : 365;
        if (remainingDays < daysInYear) {
            break;
        }
        remainingDays -= daysInYear;
        year++;
    }

    // Find month
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    for (month = 0; month < 12; month++) {
        unsigned dim = daysInMonth[month];
        if (month == 1 && isLeap) {
            dim = 29;
        }
        if (remainingDays < dim) {
            break;
        }
        remainingDays -= dim;
    }

    dayOfMonth = remainingDays + 1;

    // Day and month names
    static const char* dayNames[] = {
        "Sunday", "Monday", "Tuesday", "Wednesday",
        "Thursday", "Friday", "Saturday"
    };
    static const char* monthNames[] = {
        "January", "February", "March", "April", "May", "June",
        "July", "August", "September", "October", "November", "December"
    };

    // Format date string
    CString dateStr;
    dateStr.Format("%s, %s %u, %u",
                   dayNames[dayOfWeek],
                   monthNames[month],
                   dayOfMonth,
                   year);
    strncpy(m_dateBuffer, (const char*)dateStr, sizeof(m_dateBuffer) - 1);
    m_dateBuffer[sizeof(m_dateBuffer) - 1] = '\0';

    lv_label_set_text(m_pDateLabel, m_dateBuffer);
}

} // namespace mm
