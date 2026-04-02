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
	// Two scenarios:
	//   Scenario 1: iframe shows card number input directly (simple form)
	//   Scenario 2: iframe shows "Add credit card" option (need to add card first)
	L.Info(email, "等待信用卡表单 iframe 加载...")

	var cardFrame playwright.Frame
	scenario := 0 // 1 = direct form, 2 = add card flow
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

		// Check scenario 1: card number input exists
		input := cardFrame.Locator(`input[inputmode="numeric"]`).First()
		if cnt, _ := input.Count(); cnt > 0 {
			scenario = 1
			break
		}

		// Check scenario 2: "Add credit card" text exists
		addCardResult, _ := cardFrame.Evaluate(`() => {
			const els = document.querySelectorAll('*');
			for (const el of els) {
				if (el.children.length === 0 && el.textContent.trim() === 'Add credit card') return true;
			}
			return false;
		}`)
		if addCardResult != nil && addCardResult.(bool) {
			scenario = 2
			break
		}

		// Every 20s, reload
		if attempt > 0 && attempt%10 == 0 {
			L.Warn(email, "iframe 表单未加载, 刷新页面重试...")
			agePage.Reload()
			time.Sleep(3 * time.Second)
			cardFrame = nil
		}
	}
	if cardFrame == nil || scenario == 0 {
		return fmt.Errorf("age_verify: payments iframe not ready after 60s, current: %s", agePage.URL())
	}

	L.Info(email, fmt.Sprintf("检测到场景 %d", scenario))

	// Scenario 2: need to add card, select country, then fill form
	if scenario == 2 {
		if err := doAgeVerifyScenario2(email, agePage, cardFrame, cfg); err != nil {
			return err
		}
		return nil
	}

	// Scenario 1: direct card form — check country dropdown first
	// If country is not United States, select it (may cause page refresh)
	cardFrame, err = selectUSCountryIfNeeded(email, agePage, cardFrame)
	if err != nil {
		return err
	}

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

// doAgeVerifyScenario2 handles the "Add credit card" flow:
//  1. Click "Add credit card" in iframe
//  2. Select "United States" country → page refreshes
//  3. Fill card number, MM/YY, CVV, zip
//  4. Click "Save card"
//  5. Click "Accept"
//  6. Wait for result page
func doAgeVerifyScenario2(email string, agePage playwright.Page, cardFrame playwright.Frame, cfg config.Config) error {
	// Step 1: Click "Add credit card"
	L.Info(email, "点击 Add credit card...")
	_, err := cardFrame.Evaluate(`() => {
		const items = document.querySelectorAll('.e0DnHe');
		for (const item of items) {
			if (item.textContent.includes('Add credit card')) {
				item.click();
				return true;
			}
		}
		return false;
	}`)
	if err != nil {
		return fmt.Errorf("age_verify: click Add credit card failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Step 2: Select United States country (reuse shared helper)
	newFrame, err := selectUSCountryIfNeeded(email, agePage, cardFrame)
	if err != nil {
		return err
	}

	// Step 4: Fill card details
	L.Info(email, "填写信用卡信息...")
	cardInput := newFrame.Locator(`input[inputmode="numeric"]`).First()
	if cnt, _ := cardInput.Count(); cnt == 0 {
		return fmt.Errorf("age_verify: card number input not found after refresh")
	}
	if err := cardInput.Fill(cfg.CardNumber); err != nil {
		return fmt.Errorf("age_verify: fill card number failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill expiry
	expiryInput := newFrame.Locator(`input[aria-label*="Expiration"]`)
	if cnt, _ := expiryInput.Count(); cnt == 0 {
		expiryInput = newFrame.Locator(`input[placeholder="MM/YY"]`)
	}
	if err := expiryInput.First().Fill(cfg.CardExpiry); err != nil {
		return fmt.Errorf("age_verify: fill expiry failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill CVV
	cvvInput := newFrame.Locator(`input[aria-label*="Security code"], input[aria-label*="security code"]`).First()
	if cnt, _ := cvvInput.Count(); cnt == 0 {
		cvvInput = newFrame.Locator(`input[inputmode="numeric"]`).Nth(2)
	}
	if err := cvvInput.Fill(cfg.CardCVV); err != nil {
		return fmt.Errorf("age_verify: fill CVV failed: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Fill zip
	zipInput := newFrame.Locator(`input[autocomplete="postal-code"]`)
	if cnt, _ := zipInput.Count(); cnt == 0 {
		zipInput = newFrame.Locator(`input[inputmode="tel"]`).Last()
	}
	if err := zipInput.Fill("97201"); err != nil {
		return fmt.Errorf("age_verify: fill zip failed: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Step 5: Click "Save card"
	L.Info(email, "点击 Save card...")
	_, jsErr := newFrame.Evaluate(`() => {
		const btns = document.querySelectorAll('button[jsname="LgbsSe"]');
		for (const b of btns) {
			const span = b.querySelector('span.VfPpkd-vQzf8d');
			if (span && span.textContent.trim() === 'Save card') {
				b.click();
				return true;
			}
		}
		return false;
	}`)
	if jsErr != nil {
		return fmt.Errorf("age_verify: click Save card failed: %w", jsErr)
	}
	time.Sleep(5 * time.Second)

	// Step 6: Wait for card to be saved, then click "Accept"
	L.Info(email, "等待卡片保存, 点击 Accept...")
	for attempt := 0; attempt < 20; attempt++ {
		time.Sleep(2 * time.Second)

		// Re-find iframe (may have changed)
		for _, frame := range agePage.Frames() {
			if strings.Contains(frame.URL(), "payments.google.com") {
				newFrame = frame
				break
			}
		}

		// Try clicking Accept
		result, _ := newFrame.Evaluate(`() => {
			const btns = document.querySelectorAll('button[jsname="LgbsSe"]');
			for (const b of btns) {
				const span = b.querySelector('span.VfPpkd-vQzf8d');
				if (span && span.textContent.trim() === 'Accept') {
					b.click();
					return true;
				}
			}
			return false;
		}`)
		if result != nil && result.(bool) {
			L.Info(email, "已点击 Accept, 等待验证结果...")
			break
		}
	}

	// Step 7: Wait for result page
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		currentURL := agePage.URL()
		if strings.Contains(currentURL, "age-verification/result") {
			L.OK(email, "年龄验证成功")
			return nil
		}
		successH1 := agePage.Locator(`h1[jsname="r4nke"]`)
		if cnt, _ := successH1.Count(); cnt > 0 {
			txt, _ := successH1.TextContent()
			if strings.Contains(txt, "Your age is verified") {
				L.OK(email, "年龄验证成功")
				return nil
			}
		}
	}

	return fmt.Errorf("age_verify: scenario2 超时未获得验证结果, 当前页面: %s", agePage.URL())
}

// selectUSCountryIfNeeded checks for a country dropdown in the iframe.
// If it exists and is not "United States", selects US and waits for page refresh.
// Returns the (possibly new) iframe frame after refresh.
func selectUSCountryIfNeeded(email string, agePage playwright.Page, cardFrame playwright.Frame) (playwright.Frame, error) {
	// Check if there's a country combobox showing non-US country
	isUS, _ := cardFrame.Evaluate(`() => {
		const cbs = document.querySelectorAll('[role="combobox"]');
		for (const cb of cbs) {
			const label = cb.querySelector('.VfPpkd-uusGie-fmcmS');
			if (label && label.textContent.trim() === 'United States') return true;
			if (label && label.textContent.trim() !== '') return false;
		}
		return true; // no dropdown found, assume US
	}`)

	if isUS != nil && isUS.(bool) {
		return cardFrame, nil // already US or no dropdown
	}

	L.Info(email, "国家非美国, 切换到 United States...")

	// Click country dropdown to open
	clickResult, _ := cardFrame.Evaluate(`() => {
		const cbs = document.querySelectorAll('[role="combobox"]');
		for (const cb of cbs) {
			const label = cb.querySelector('.VfPpkd-uusGie-fmcmS');
			if (label && label.textContent.trim() !== '') {
				cb.click();
				return 'clicked: ' + label.textContent.trim();
			}
		}
		return 'no combobox found';
	}`)
	L.Info(email, fmt.Sprintf("国家下拉框: %v", clickResult))
	time.Sleep(1 * time.Second)

	// Select "United States"
	selectResult, _ := cardFrame.Evaluate(`() => {
		const opts = document.querySelectorAll('[role="option"]');
		for (const opt of opts) {
			if (opt.textContent.trim() === 'United States') {
				opt.click();
				return true;
			}
		}
		return false;
	}`)
	L.Info(email, fmt.Sprintf("选择 United States: %v", selectResult))
	time.Sleep(5 * time.Second) // wait for refresh

	// Re-find iframe after refresh. Page may have navigated away from credit-card.
	var newFrame playwright.Frame
	for attempt := 0; attempt < 20; attempt++ {
		time.Sleep(2 * time.Second)

		// If page navigated back to age-verification (without /credit-card), re-click the link
		currentURL := agePage.URL()
		if strings.Contains(currentURL, "age-verification") && !strings.Contains(currentURL, "credit-card") {
			L.Info(email, "页面刷新回年龄验证首页, 重新点击信用卡选项...")
			creditCardLink := agePage.Locator(`a[href="age-verification/credit-card"]`)
			if cnt, _ := creditCardLink.Count(); cnt > 0 {
				_ = creditCardLink.Click()
				time.Sleep(3 * time.Second)
			}
		}

		for _, frame := range agePage.Frames() {
			if strings.Contains(frame.URL(), "payments.google.com") {
				newFrame = frame
				break
			}
		}
		if newFrame == nil {
			continue
		}
		input := newFrame.Locator(`input[inputmode="numeric"]`).First()
		if cnt, _ := input.Count(); cnt > 0 {
			L.Info(email, "国家已切换为 United States, iframe 重新加载完成")
			return newFrame, nil
		}
	}

	if newFrame != nil {
		return newFrame, nil
	}
	return nil, fmt.Errorf("age_verify: iframe not found after country change")
}
