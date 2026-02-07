#include "modules/widgets/widget_base.h"

namespace mm {

WidgetBase::WidgetBase(const char* name, lv_obj_t* parent, CTimer* timer)
    : m_pName(name),
      m_pParent(parent),
      m_pContainer(nullptr),
      m_pTimer(timer),
      m_screenWidth(1920),
      m_screenHeight(1080),
      m_x(0),
      m_y(0),
      m_width(0),
      m_height(0),
      m_visible(true)
{
    m_gridPos.col = 0;
    m_gridPos.row = 0;
    m_gridPos.colSpan = 1;
    m_gridPos.rowSpan = 1;

    // Create container for this widget
    m_pContainer = lv_obj_create(parent);
    lv_obj_set_style_bg_opa(m_pContainer, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(m_pContainer, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(m_pContainer, 0, LV_PART_MAIN);
    lv_obj_clear_flag(m_pContainer, LV_OBJ_FLAG_SCROLLABLE);
}

WidgetBase::~WidgetBase()
{
    if (m_pContainer) {
        lv_obj_delete(m_pContainer);
        m_pContainer = nullptr;
    }
}

void WidgetBase::SetGridPosition(int col, int row, int colSpan, int rowSpan)
{
    m_gridPos.col = col;
    m_gridPos.row = row;
    m_gridPos.colSpan = colSpan;
    m_gridPos.rowSpan = rowSpan;
    UpdateBounds();
}

void WidgetBase::SetScreenSize(int width, int height)
{
    m_screenWidth = width;
    m_screenHeight = height;
    UpdateBounds();
}

void WidgetBase::SetAbsolutePosition(int x, int y, int width, int height)
{
    m_x = x;
    m_y = y;
    m_width = width;
    m_height = height;

    if (m_pContainer) {
        lv_obj_set_pos(m_pContainer, m_x, m_y);
        lv_obj_set_size(m_pContainer, m_width, m_height);
    }
}

void WidgetBase::SetContentSize()
{
    // Set widget to size itself to content (width: 100%, height: fit content)
    if (m_pContainer) {
        lv_obj_set_width(m_pContainer, lv_pct(100));
        lv_obj_set_height(m_pContainer, LV_SIZE_CONTENT);
    }
}

void WidgetBase::SetFillHeight()
{
    // Set widget to fill remaining height in a flex column layout
    if (m_pContainer) {
        lv_obj_set_width(m_pContainer, lv_pct(100));
        lv_obj_set_flex_grow(m_pContainer, 1);
    }
}

void WidgetBase::SetVisible(bool visible)
{
    m_visible = visible;
    if (m_pContainer) {
        if (visible) {
            lv_obj_clear_flag(m_pContainer, LV_OBJ_FLAG_HIDDEN);
        } else {
            lv_obj_add_flag(m_pContainer, LV_OBJ_FLAG_HIDDEN);
        }
    }
}

void WidgetBase::UpdateBounds()
{
    // Calculate cell size
    int usableWidth = m_screenWidth - (2 * GRID_PADDING) - ((GRID_COLS - 1) * GRID_GAP);
    int usableHeight = m_screenHeight - (2 * GRID_PADDING) - ((GRID_ROWS - 1) * GRID_GAP);

    int cellWidth = usableWidth / GRID_COLS;
    int cellHeight = usableHeight / GRID_ROWS;

    // Calculate position and size
    m_x = GRID_PADDING + (m_gridPos.col * (cellWidth + GRID_GAP));
    m_y = GRID_PADDING + (m_gridPos.row * (cellHeight + GRID_GAP));
    m_width = (m_gridPos.colSpan * cellWidth) + ((m_gridPos.colSpan - 1) * GRID_GAP);
    m_height = (m_gridPos.rowSpan * cellHeight) + ((m_gridPos.rowSpan - 1) * GRID_GAP);

    // Update container position and size
    if (m_pContainer) {
        lv_obj_set_pos(m_pContainer, m_x, m_y);
        lv_obj_set_size(m_pContainer, m_width, m_height);
    }
}

} // namespace mm
