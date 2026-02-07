#include "ui/grid.h"

namespace mm {

Grid::Grid(Display* display, const GridConfig& config)
    : m_pDisplay(display),
      m_nColumns(config.columns),
      m_nRows(config.rows),
      m_nPaddingX(config.paddingX),
      m_nPaddingY(config.paddingY),
      m_nGapX(config.gapX),
      m_nGapY(config.gapY)
{
    int availableWidth = m_pDisplay->GetWidth() - 2 * m_nPaddingX -
                         (m_nColumns - 1) * m_nGapX;
    int availableHeight = m_pDisplay->GetHeight() - 2 * m_nPaddingY -
                          (m_nRows - 1) * m_nGapY;

    m_nCellWidth = availableWidth / m_nColumns;
    m_nCellHeight = availableHeight / m_nRows;
}

Rect Grid::GetCellRect(int gridX, int gridY, int spanX, int spanY) const
{
    int x = m_nPaddingX + gridX * (m_nCellWidth + m_nGapX);
    int y = m_nPaddingY + gridY * (m_nCellHeight + m_nGapY);

    int width = spanX * m_nCellWidth + (spanX - 1) * m_nGapX;
    int height = spanY * m_nCellHeight + (spanY - 1) * m_nGapY;

    return {x, y, width, height};
}

void Grid::DrawDebugGrid(const Color& color)
{
    for (int row = 0; row < m_nRows; ++row) {
        for (int col = 0; col < m_nColumns; ++col) {
            Rect cell = GetCellRect(col, row);
            m_pDisplay->DrawRect(cell, color);
        }
    }
}

} // namespace mm
