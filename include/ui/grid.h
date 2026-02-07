#ifndef GRID_H
#define GRID_H

#include "ui/display.h"
#include "config/config.h"

namespace mm {

class Grid
{
public:
    Grid(Display* display, const GridConfig& config);
    ~Grid() {}

    Rect GetCellRect(int gridX, int gridY, int spanX = 1, int spanY = 1) const;

    int GetCellWidth() const { return m_nCellWidth; }
    int GetCellHeight() const { return m_nCellHeight; }
    int GetColumns() const { return m_nColumns; }
    int GetRows() const { return m_nRows; }

    void DrawDebugGrid(const Color& color);

private:
    Display*    m_pDisplay;
    int         m_nColumns;
    int         m_nRows;
    int         m_nPaddingX;
    int         m_nPaddingY;
    int         m_nGapX;
    int         m_nGapY;
    int         m_nCellWidth;
    int         m_nCellHeight;
};

} // namespace mm

#endif // GRID_H
