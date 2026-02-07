#ifndef DATETIME_WIDGET_H
#define DATETIME_WIDGET_H

#include "modules/widgets/widget_base.h"

namespace mm {

class DateTimeWidget : public WidgetBase
{
public:
    DateTimeWidget(lv_obj_t* parent, CTimer* timer);
    ~DateTimeWidget() override;

    bool Initialize() override;
    void Update() override;

    // Set timezone offset in seconds from UTC
    void SetTimezoneOffset(int offsetSeconds) { m_timezoneOffset = offsetSeconds; }

private:
    void CreateUI();
    void UpdateTime();
    void UpdateDate();

    // LVGL objects
    lv_obj_t*   m_pDateLabel;
    lv_obj_t*   m_pTimeLabel;
    lv_obj_t*   m_pSecondsLabel;
    lv_obj_t*   m_pAmPmLabel;

    // State
    int         m_timezoneOffset;
    unsigned    m_lastUpdateTime;

    // Buffers for formatted text
    char        m_dateBuffer[64];
    char        m_timeBuffer[16];
    char        m_secondsBuffer[8];
    char        m_ampmBuffer[4];
};

} // namespace mm

#endif // DATETIME_WIDGET_H
