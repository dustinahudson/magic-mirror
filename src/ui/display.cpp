#include "ui/display.h"
#include <circle/util.h>
#include <circle/bcmframebuffer.h>
#include <string.h>

namespace mm {

boolean Rect::Contains(int px, int py) const
{
    return px >= x && px < x + width && py >= y && py < y + height;
}

Rect Rect::Inset(int amount) const
{
    return {x + amount, y + amount, width - 2 * amount, height - 2 * amount};
}

Display::Display(CScreenDevice* screen)
    : m_pScreen(screen),
      m_pFrameBuffer(0),
      m_pBcmFrameBuffer(0),
      m_nWidth(0),
      m_nHeight(0)
{
}

Display::~Display()
{
}

boolean Display::Initialize()
{
    if (!m_pScreen) {
        return FALSE;
    }

    m_nWidth = m_pScreen->GetWidth();
    m_nHeight = m_pScreen->GetHeight();

    m_pBcmFrameBuffer = m_pScreen->GetFrameBuffer();
    if (m_pBcmFrameBuffer) {
        m_pFrameBuffer = (u32*)(uintptr)m_pBcmFrameBuffer->GetBuffer();
    }

    return m_pFrameBuffer != 0;
}

void Display::Clear(const Color& color)
{
    if (!m_pFrameBuffer) {
        return;
    }

    u32 pixelValue = color.ToARGB8888();
    int pixels = m_nWidth * m_nHeight;

    if (pixelValue == 0xFF000000 || pixelValue == 0x00000000) {
        memset(m_pFrameBuffer, 0, pixels * sizeof(u32));
    } else {
        u32* p = m_pFrameBuffer;
        u32* end = p + pixels;
        while (p < end) {
            *p++ = pixelValue;
        }
    }
}

void Display::Present()
{
    // No-op - direct framebuffer is immediately visible
}

void Display::DrawPixel(int x, int y, const Color& color)
{
    if (x < 0 || x >= m_nWidth || y < 0 || y >= m_nHeight) {
        return;
    }

    int index = y * m_nWidth + x;

    if (color.a == 255) {
        m_pFrameBuffer[index] = color.ToARGB8888();
    } else if (color.a > 0) {
        u32 dst = m_pFrameBuffer[index];
        u8 dstR = (dst >> 16) & 0xFF;
        u8 dstG = (dst >> 8) & 0xFF;
        u8 dstB = dst & 0xFF;

        u8 alpha = color.a;
        u8 invAlpha = 255 - alpha;

        u8 r = (color.r * alpha + dstR * invAlpha) / 255;
        u8 g = (color.g * alpha + dstG * invAlpha) / 255;
        u8 b = (color.b * alpha + dstB * invAlpha) / 255;

        m_pFrameBuffer[index] = (0xFF << 24) | (r << 16) | (g << 8) | b;
    }
}

void Display::DrawRect(const Rect& rect, const Color& color)
{
    for (int x = rect.x; x < rect.x + rect.width; ++x) {
        DrawPixel(x, rect.y, color);
        DrawPixel(x, rect.y + rect.height - 1, color);
    }
    for (int y = rect.y; y < rect.y + rect.height; ++y) {
        DrawPixel(rect.x, y, color);
        DrawPixel(rect.x + rect.width - 1, y, color);
    }
}

void Display::FillRect(const Rect& rect, const Color& color)
{
    int x1 = rect.x < 0 ? 0 : rect.x;
    int y1 = rect.y < 0 ? 0 : rect.y;
    int x2 = rect.x + rect.width > m_nWidth ? m_nWidth : rect.x + rect.width;
    int y2 = rect.y + rect.height > m_nHeight ? m_nHeight : rect.y + rect.height;

    u32 pixelValue = color.ToARGB8888();

    if (color.a == 255) {
        for (int y = y1; y < y2; ++y) {
            int rowStart = y * m_nWidth;
            for (int x = x1; x < x2; ++x) {
                m_pFrameBuffer[rowStart + x] = pixelValue;
            }
        }
    } else {
        for (int y = y1; y < y2; ++y) {
            for (int x = x1; x < x2; ++x) {
                DrawPixel(x, y, color);
            }
        }
    }
}

} // namespace mm
