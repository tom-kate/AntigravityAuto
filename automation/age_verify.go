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
	if cfg.CardNumber == "" || cfg.CardExpiry == "" || cfg.CardCVV == "" {
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
	if cnt, _ := creditCardLink.Count(); cnt == 0 {
		L.Fail(email, "年龄验证: 未找到信用卡选项, 页面不符合预期")
		return errAgeVerification
	}
	if err := creditCardLink.Click(); err != nil {
		return fmt.Errorf("age_verify: click credit card option failed: %w", err)
	}

	// The credit card form is inside a payments.google.com iframe.
	// Wait for the iframe to appear and the form inside it to load.
	L.Info(email, "等待信用卡表单 iframe 加载...")

	var cardFrame playwright.Frame
	for attempt := 0; attempt < 30; attempt++ {
		time.Sleep(2 * time.Second)

		// Look for the payments iframe
		for _, frame := range agePage.Frames() {
			if strings.Contains(frame.URL(), "payments.google.com") {
				cardFrame = frame
				break
			}
		}
		if cardFrame == nil {
			continue
		}

		// Check if the card number input is ready inside the iframe
		input := cardFrame.Locator(`input[inputmode="numeric"]`).First()
		if cnt, _ := input.Count(); cnt > 0 {
			break
		}

		// Every 20s, reload to force re-render
		if attempt > 0 && attempt%10 == 0 {
			L.Warn(email, "iframe 表单未加载, 刷新页面重试...")
			agePage.Reload()
			time.Sleep(3 * time.Second)
			cardFrame = nil
		}
	}
	if cardFrame == nil {
		return fmt.Errorf("age_verify: payments iframe not found after 60s, current: %s", agePage.URL())
	}

	// Verify the card input is present
	cardNumberInput := cardFrame.Locator(`input[inputmode="numeric"]`).First()
	if cnt, _ := cardNumberInput.Count(); cnt == 0 {
		return fmt.Errorf("age_verify: card number input not found inside iframe")
	}
	time.Sleep(1 * time.Second)

	// Fill card number
	L.Info(email, "填写信用卡信息...")
	if err := cardNumberInput.Fill(cfg.CardNumber); err != nil {
		return fmt.Errorf("age_verify: fill card number failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill expiry (MM/YY)
	expiryInput := cardFrame.Locator(`input[aria-label*="Expiration"]`)
	if cnt, _ := expiryInput.Count(); cnt == 0 {
		// Fallback: find by placeholder text
		expiryInput = cardFrame.Locator(`input[placeholder="MM/YY"]`)
	}
	if err := expiryInput.First().Fill(cfg.CardExpiry); err != nil {
		return fmt.Errorf("age_verify: fill expiry failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill CVV (Security code)
	cvvInput := cardFrame.Locator(`input[aria-label*="Security code"], input[aria-label*="security code"], input[aria-label*="CVC"], input[aria-label*="CVV"]`).First()
	if cnt, _ := cvvInput.Count(); cnt == 0 {
		// Fallback: third numeric input (after card number and expiry)
		cvvInput = cardFrame.Locator(`input[inputmode="numeric"]`).Nth(2)
	}
	if err := cvvInput.Fill(cfg.CardCVV); err != nil {
		return fmt.Errorf("age_verify: fill CVV failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill zip code — fixed value 97201
	zipInput := cardFrame.Locator(`input[autocomplete="postal-code"]`)
	if cnt, _ := zipInput.Count(); cnt == 0 {
		zipInput = cardFrame.Locator(`input[inputmode="tel"]`).Last()
	}
	if err := zipInput.Fill("97201"); err != nil {
		return fmt.Errorf("age_verify: fill zip failed: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Click "Save and submit" button (inside the iframe)
	// Multiple buttons share jsname="LgbsSe", use JS to find by exact text
	L.Info(email, "提交年龄验证...")
	_, jsErr := cardFrame.Evaluate(`() => {
		const btns = document.querySelectorAll('button[jsname="LgbsSe"]');
		for (const b of btns) {
			const span = b.querySelector('span.VfPpkd-vQzf8d');
			if (span && span.textContent.trim() === 'Save and submit') {
				b.click();
				return true;
			}
		}
		return false;
	}`)
	if jsErr != nil {
		return fmt.Errorf("age_verify: click submit failed: %w", jsErr)
	}

	// Wait for result
	L.Info(email, "等待验证结果...")
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)

		// Check URL for result page
		currentURL := agePage.URL()
		if strings.Contains(currentURL, "age-verification/result") {
			L.OK(email, "年龄验证成功")
			return nil
		}

		// Check for success text: "Your age is verified"
		successH1 := agePage.Locator(`h1[jsname="r4nke"]`)
		if cnt, _ := successH1.Count(); cnt > 0 {
			txt, _ := successH1.TextContent()
			if strings.Contains(txt, "Your age is verified") {
				L.OK(email, "年龄验证成功")
				return nil
			}
		}

		// Check for error inside iframe
		errDiv := cardFrame.Locator(`div[role="alert"], div.error-message`)
		if cnt, _ := errDiv.Count(); cnt > 0 {
			txt, _ := errDiv.First().TextContent()
			if txt != "" {
				L.Fail(email, fmt.Sprintf("年龄验证失败: %s", txt))
				return errAgeVerification
			}
		}
	}

	return fmt.Errorf("age_verify: 超时未获得验证结果, 当前页面: %s", agePage.URL())
}
