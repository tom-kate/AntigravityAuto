package automation

import (
	"fmt"
	"time"

	L "antiauto/logger"

	"github.com/playwright-community/playwright-go"
)

// checkAgeVerification opens the age verification page and checks if age verification is needed.
// Returns (true, nil) if verification is needed, (false, nil) if not needed.
// Detection is language-independent: checks for the success SVG image instead of text.
func checkAgeVerification(email string, page playwright.Page) (bool, error) {
	agePage, err := page.Context().NewPage()
	if err != nil {
		return false, fmt.Errorf("age_check: 创建新标签页失败: %w", err)
	}
	defer agePage.Close()

	L.Info(email, "打开年龄验证页面...")
	if _, err := agePage.Goto("https://myaccount.google.com/age-verification", playwright.PageGotoOptions{
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return false, fmt.Errorf("age_check: 导航失败: %w", err)
	}
	if err := agePage.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return false, fmt.Errorf("age_check: 等待加载失败: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Check for the success SVG image — if present, age verification is NOT needed
	// This is language-independent: the SVG src is always the same regardless of account language
	successImg := agePage.Locator(`img[src*="ageui/success"]`)
	if cnt, _ := successImg.Count(); cnt > 0 {
		return false, nil
	}

	// If no success image found, age verification IS needed
	return true, nil
}
