#include "ui/font_renderer.h"
#include <fatfs/ff.h>
#include <cstring>
#include <cmath>

#define STB_TRUETYPE_IMPLEMENTATION
#include "stb_truetype.h"

namespace mm {

Font::Font()
    : m_fontInfo(nullptr)
{
}

Font::~Font()
{
    if (m_fontInfo) {
        delete static_cast<stbtt_fontinfo*>(m_fontInfo);
    }
}

bool Font::LoadFromFile(const std::string& path)
{
    FIL file;
    if (f_open(&file, path.c_str(), FA_READ) != FR_OK) {
        return false;
    }

    UINT fileSize = f_size(&file);
    m_fontData.resize(fileSize);

    UINT bytesRead;
    FRESULT result = f_read(&file, m_fontData.data(), fileSize, &bytesRead);
    f_close(&file);

    if (result != FR_OK || bytesRead != fileSize) {
        return false;
    }

    return LoadFromMemory(m_fontData.data(), m_fontData.size());
}

bool Font::LoadFromMemory(const unsigned char* data, size_t size)
{
    m_fontData.assign(data, data + size);

    m_fontInfo = new stbtt_fontinfo;
    auto* info = static_cast<stbtt_fontinfo*>(m_fontInfo);

    if (!stbtt_InitFont(info, m_fontData.data(),
                        stbtt_GetFontOffsetForIndex(m_fontData.data(), 0))) {
        delete info;
        m_fontInfo = nullptr;
        return false;
    }

    return true;
}

FontMetrics Font::GetMetrics(float size) const
{
    FontMetrics metrics = {0, 0, 0};

    if (!m_fontInfo) {
        return metrics;
    }

    auto* info = static_cast<stbtt_fontinfo*>(m_fontInfo);
    float scale = stbtt_ScaleForPixelHeight(info, size);

    int ascent, descent, lineGap;
    stbtt_GetFontVMetrics(info, &ascent, &descent, &lineGap);

    metrics.ascent = static_cast<int>(ascent * scale);
    metrics.descent = static_cast<int>(descent * scale);
    metrics.lineHeight = static_cast<int>((ascent - descent + lineGap) * scale);

    return metrics;
}

int Font::GetTextWidth(const std::string& text, float size) const
{
    if (!m_fontInfo || text.empty()) {
        return 0;
    }

    auto* info = static_cast<stbtt_fontinfo*>(m_fontInfo);
    float scale = stbtt_ScaleForPixelHeight(info, size);

    int width = 0;
    for (size_t i = 0; i < text.length(); ++i) {
        int advanceWidth, leftSideBearing;
        stbtt_GetCodepointHMetrics(info, text[i], &advanceWidth, &leftSideBearing);
        width += static_cast<int>(advanceWidth * scale);

        if (i < text.length() - 1) {
            int kern = stbtt_GetCodepointKernAdvance(info, text[i], text[i + 1]);
            width += static_cast<int>(kern * scale);
        }
    }

    return width;
}

int Font::GetTextHeight(float size) const
{
    FontMetrics metrics = GetMetrics(size);
    return metrics.ascent - metrics.descent;
}

// FontRenderer implementation

FontRenderer::FontRenderer(Display* display)
    : m_pDisplay(display)
{
}

FontRenderer::~FontRenderer()
{
}

bool FontRenderer::Initialize()
{
    return true;
}

bool FontRenderer::LoadFont(const std::string& name, const std::string& path)
{
    auto font = std::make_unique<Font>();
    if (!font->LoadFromFile(path)) {
        return false;
    }

    m_fonts[name] = std::move(font);
    return true;
}

Font* FontRenderer::GetFont(const std::string& name)
{
    auto it = m_fonts.find(name);
    if (it != m_fonts.end()) {
        return it->second.get();
    }
    return nullptr;
}

void FontRenderer::DrawText(const std::string& text, int x, int y,
                            const std::string& fontName, float size,
                            const Color& color, TextAlign align,
                            TextBaseline baseline)
{
    Font* font = GetFont(fontName);
    if (!font || text.empty()) {
        return;
    }

    // Handle alignment
    if (align != TextAlign::Left) {
        int textWidth = font->GetTextWidth(text, size);
        if (align == TextAlign::Center) {
            x -= textWidth / 2;
        } else if (align == TextAlign::Right) {
            x -= textWidth;
        }
    }

    // Handle baseline
    FontMetrics metrics = font->GetMetrics(size);
    if (baseline == TextBaseline::Middle) {
        y -= (metrics.ascent + metrics.descent) / 2;
    } else if (baseline == TextBaseline::Bottom) {
        y -= metrics.ascent;
    }

    // Render each character
    auto* info = static_cast<stbtt_fontinfo*>(font->GetFontInfo());
    float scale = stbtt_ScaleForPixelHeight(info, size);

    int cursorX = x;
    for (size_t i = 0; i < text.length(); ++i) {
        RenderGlyph(font, text[i], size, cursorX, y + metrics.ascent, color);

        int advanceWidth, leftSideBearing;
        stbtt_GetCodepointHMetrics(info, text[i], &advanceWidth, &leftSideBearing);
        cursorX += static_cast<int>(advanceWidth * scale);

        if (i < text.length() - 1) {
            int kern = stbtt_GetCodepointKernAdvance(info, text[i], text[i + 1]);
            cursorX += static_cast<int>(kern * scale);
        }
    }
}

void FontRenderer::DrawTextInRect(const std::string& text, const Rect& rect,
                                  const std::string& fontName, float size,
                                  const Color& color, TextAlign align,
                                  TextBaseline baseline)
{
    int x = rect.x;
    int y = rect.y;

    if (align == TextAlign::Center) {
        x = rect.x + rect.width / 2;
    } else if (align == TextAlign::Right) {
        x = rect.x + rect.width;
    }

    if (baseline == TextBaseline::Middle) {
        y = rect.y + rect.height / 2;
    } else if (baseline == TextBaseline::Bottom) {
        y = rect.y + rect.height;
    }

    DrawText(text, x, y, fontName, size, color, align, baseline);
}

int FontRenderer::MeasureTextWidth(const std::string& text,
                                   const std::string& fontName, float size)
{
    Font* font = GetFont(fontName);
    if (!font) {
        return 0;
    }
    return font->GetTextWidth(text, size);
}

int FontRenderer::MeasureTextHeight(const std::string& fontName, float size)
{
    Font* font = GetFont(fontName);
    if (!font) {
        return 0;
    }
    return font->GetTextHeight(size);
}

void FontRenderer::RenderGlyph(Font* font, int codepoint, float size,
                               int x, int y, const Color& color)
{
    auto* info = static_cast<stbtt_fontinfo*>(font->GetFontInfo());
    float scale = stbtt_ScaleForPixelHeight(info, size);

    int width, height, xoff, yoff;
    unsigned char* bitmap = stbtt_GetCodepointBitmap(
        info, scale, scale, codepoint, &width, &height, &xoff, &yoff);

    if (!bitmap) {
        return;
    }

    // Render bitmap to display
    for (int py = 0; py < height; ++py) {
        for (int px = 0; px < width; ++px) {
            unsigned char alpha = bitmap[py * width + px];
            if (alpha > 0) {
                Color pixelColor = color;
                pixelColor.a = (color.a * alpha) / 255;
                m_pDisplay->DrawPixel(x + xoff + px, y + yoff + py, pixelColor);
            }
        }
    }

    stbtt_FreeBitmap(bitmap, nullptr);
}

} // namespace mm
