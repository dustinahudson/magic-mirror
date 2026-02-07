#ifndef DISPLAY_H
#define DISPLAY_H

#include <circle/screen.h>
#include <circle/bcmframebuffer.h>
#include <circle/types.h>

namespace mm {

struct Color {
    u8 r, g, b, a;

    static Color Black()   { return {0, 0, 0, 255}; }
    static Color White()   { return {255, 255, 255, 255}; }
    static Color Gray(u8 v) { return {v, v, v, 255}; }
    static Color FromRGB(u8 r, u8 g, u8 b) { return {r, g, b, 255}; }
    static Color FromRGBA(u8 r, u8 g, u8 b, u8 a) { return {r, g, b, a}; }

    u32 ToARGB8888() const { return (a << 24) | (r << 16) | (g << 8) | b; }
};

struct Rect {
    int x, y, width, height;

    boolean Contains(int px, int py) const;
    Rect Inset(int amount) const;
};

class Display
{
public:
    Display(CScreenDevice* screen);
    ~Display();

    boolean Initialize();

    int GetWidth() const { return m_nWidth; }
    int GetHeight() const { return m_nHeight; }

    void Clear(const Color& color = Color::Black());
    void Present();

    void DrawPixel(int x, int y, const Color& color);
    void DrawRect(const Rect& rect, const Color& color);
    void FillRect(const Rect& rect, const Color& color);

    u32* GetFrameBuffer() { return m_pFrameBuffer; }

private:
    CScreenDevice*  m_pScreen;
    u32*            m_pFrameBuffer;
    CBcmFrameBuffer* m_pBcmFrameBuffer;
    int             m_nWidth;
    int             m_nHeight;
};

} // namespace mm

#endif // DISPLAY_H
