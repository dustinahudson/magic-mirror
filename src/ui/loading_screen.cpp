#include "ui/loading_screen.h"

namespace mm {

LoadingScreen::LoadingScreen(Display* display, FontRenderer* fontRenderer)
    : m_pDisplay(display),
      m_pFontRenderer(fontRenderer),
      m_visible(false)
{
}

void LoadingScreen::Show()
{
    m_visible = true;
    m_currentModule = "";
    Render();
}

void LoadingScreen::UpdateStatus(const std::string& moduleName)
{
    m_currentModule = moduleName;
    if (m_visible) {
        Render();
    }
}

void LoadingScreen::Hide()
{
    m_visible = false;
}

void LoadingScreen::Render()
{
    if (!m_visible) {
        return;
    }

    // Clear screen to black
    m_pDisplay->Clear(Color::Black());

    int screenWidth = m_pDisplay->GetWidth();
    int screenHeight = m_pDisplay->GetHeight();

    // Draw "Loading" text centered
    const float loadingFontSize = 48.0f;
    const std::string loadingText = "Loading";

    if (m_pFontRenderer) {
        int textWidth = m_pFontRenderer->MeasureTextWidth(
            loadingText, "regular", loadingFontSize);
        int textHeight = m_pFontRenderer->MeasureTextHeight(
            "regular", loadingFontSize);

        int x = (screenWidth - textWidth) / 2;
        int y = (screenHeight - textHeight) / 2 - 30;

        m_pFontRenderer->DrawText(loadingText, x, y, "regular",
                                  loadingFontSize, Color::White());

        // Draw current module below
        if (!m_currentModule.empty()) {
            const float moduleFontSize = 24.0f;
            int moduleWidth = m_pFontRenderer->MeasureTextWidth(
                m_currentModule, "light", moduleFontSize);
            int moduleX = (screenWidth - moduleWidth) / 2;
            int moduleY = y + textHeight + 20;

            m_pFontRenderer->DrawText(m_currentModule, moduleX, moduleY,
                                      "light", moduleFontSize,
                                      Color::Gray(150));
        }
    } else {
        // Fallback: simple rectangle to indicate loading
        Rect indicator = {
            screenWidth / 2 - 50,
            screenHeight / 2 - 10,
            100, 20
        };
        m_pDisplay->FillRect(indicator, Color::White());
    }

    m_pDisplay->Present();
}

} // namespace mm
