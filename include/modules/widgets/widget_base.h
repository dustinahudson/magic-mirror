#ifndef WIDGET_BASE_H
#define WIDGET_BASE_H

#include <circle/timer.h>
#include <circle/types.h>
#include <lvgl/lvgl/lvgl.h>

namespace mm {

// Internal grid position for widget placement
struct WidgetGridPos {
    int col;
    int row;
    int colSpan;
    int rowSpan;
};

class WidgetBase
{
public:
    WidgetBase(const char* name, lv_obj_t* parent, CTimer* timer);
    virtual ~WidgetBase();

    // Lifecycle
    virtual bool Initialize() = 0;
    virtual void Update() = 0;

    // Position on screen (grid-based)
    void SetGridPosition(int col, int row, int colSpan, int rowSpan);
    void SetScreenSize(int width, int height);

    // Position on screen (absolute)
    void SetAbsolutePosition(int x, int y, int width, int height);

    // Set widget to size itself to content (for flex/stack layouts)
    void SetContentSize();

    // Set widget to fill remaining height in flex layout
    void SetFillHeight();

    // Visibility
    bool IsVisible() const { return m_visible; }
    void SetVisible(bool visible);

    // Container access
    lv_obj_t* GetContainer() { return m_pContainer; }

protected:
    // Calculate pixel bounds from grid position
    void UpdateBounds();

    const char*     m_pName;
    lv_obj_t*       m_pParent;
    lv_obj_t*       m_pContainer;
    CTimer*         m_pTimer;

    WidgetGridPos   m_gridPos;
    int             m_screenWidth;
    int             m_screenHeight;

    // Calculated pixel bounds
    int             m_x;
    int             m_y;
    int             m_width;
    int             m_height;

    bool            m_visible;

    // Grid constants (12 columns, 16 rows for tighter stacking)
    static const int GRID_COLS = 12;
    static const int GRID_ROWS = 16;
    static const int GRID_PADDING = 20;
    static const int GRID_GAP = 5;
};

} // namespace mm

#endif // WIDGET_BASE_H
