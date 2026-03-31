package automation

import (
	"fmt"
	"strings"
	"time"

	"antiauto/config"
	L "antiauto/logger"

	"github.com/playwright-community/playwright-go"
)

// doAgeVerify performs Google age verification using a credit card.
// The page should already be logged in. Opens age-verification in a new tab.
func doAgeVerify(email string, page playwright.Page) error {
	cfg := config.Get()
	if cfg.CardNumber == "" || cfg.CardExpiry == "" || cfg.CardCVV == "" || cfg.CardZip == "" {
		L.Fail(email, "年龄验证: 信用卡信息未配置")
		return errAgeVerification
	}

	L.Info(email, "打开年龄验证页面...")
	agePage, err := page.Context().NewPage()
	if err != nil {
		return fmt.Errorf("age_verify: create new page failed: %w", err)
	}
	defer agePage.Close()

	if _, err := agePage.Goto("https://myaccount.google.com/age-verification", playwright.PageGotoOptions{
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return fmt.Errorf("age_verify: navigate failed: %w", err)
	}

	if err := agePage.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return fmt.Errorf("age_verify: wait for load failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Click "Use your credit card" link
	L.Info(email, "选择信用卡验证方式...")
	creditCardLink := agePage.Locator(`a[href="age-verification/credit-card"]`)
	if err := creditCardLink.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		return fmt.Errorf("age_verify: credit card option not found: %w", err)
	}
	if err := creditCardLink.Click(); err != nil {
		return fmt.Errorf("age_verify: click credit card option failed: %w", err)
	}

	// Wait for the credit card form to load (pjax page, wait for input fields)
	L.Info(email, "等待信用卡表单加载...")
	cardNumberInput := agePage.Locator(`input[aria-labelledby="i4"]`)
	if err := cardNumberInput.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		return fmt.Errorf("age_verify: card form not loaded: %w", err)
	}
	time.Sleep(2 * time.Second)

	// Fill card number
	L.Info(email, "填写信用卡信息...")
	if err := cardNumberInput.Fill(cfg.CardNumber); err != nil {
		return fmt.Errorf("age_verify: fill card number failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill expiry (MM/YY)
	expiryInput := agePage.Locator(`input[aria-label*="Expiration date"]`)
	if err := expiryInput.Fill(cfg.CardExpiry); err != nil {
		return fmt.Errorf("age_verify: fill expiry failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill CVV
	cvvInput := agePage.Locator(`input[aria-labelledby="i14"]`)
	if err := cvvInput.Fill(cfg.CardCVV); err != nil {
		return fmt.Errorf("age_verify: fill CVV failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill zip code
	zipInput := agePage.Locator(`input[autocomplete="postal-code"]`)
	if err := zipInput.Fill(cfg.CardZip); err != nil {
		return fmt.Errorf("age_verify: fill zip failed: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Click "Save and submit"
	L.Info(email, "提交年龄验证...")
	submitBtn := agePage.Locator(`button:has-text("Save and submit")`)
	if err := submitBtn.Click(); err != nil {
		return fmt.Errorf("age_verify: click submit failed: %w", err)
	}

	// Wait for result
	L.Info(email, "等待验证结果...")
	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Second)

		// Check for success: "Your age is verified"
		successH1 := agePage.Locator(`h1[jsname="r4nke"]`)
		if cnt, _ := successH1.Count(); cnt > 0 {
			txt, _ := successH1.TextContent()
			if strings.Contains(txt, "Your age is verified") {
				L.OK(email, "年龄验证成功")
				return nil
			}
		}

		// Check for card error: OR_MIVEM_02
		errDiv := agePage.Locator(`div[role="alert"]`)
		if cnt, _ := errDiv.Count(); cnt > 0 {
			txt, _ := errDiv.TextContent()
			if strings.Contains(txt, "OR_MIVEM_02") || strings.Contains(txt, "double-check your card") {
				L.Fail(email, "年龄验证失败: 信用卡无效 (OR_MIVEM_02)")
				return errAgeVerification
			}
		}

		// Check URL for result page
		currentURL := agePage.URL()
		if strings.Contains(currentURL, "age-verification/result") {
			L.OK(email, "年龄验证成功")
			return nil
		}
	}

	return fmt.Errorf("age_verify: 超时未获得验证结果, 当前页面: %s", agePage.URL())
}
