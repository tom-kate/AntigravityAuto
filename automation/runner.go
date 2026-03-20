package automation

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"antiauto/api"
	"antiauto/config"
	"antiauto/db"
	L "antiauto/logger"

	"github.com/pquerna/otp/totp"
	"github.com/playwright-community/playwright-go"
)

// ─── Hardcoded flow: Login → OAuth → CPA check → (phone bind → delete CPA → OAuth redo) → done ───

// Navigation timeout for slow networks (120 seconds)
const navTimeout = 120000

// errRecaptcha is a sentinel error indicating a recaptcha challenge was encountered.
var errRecaptcha = fmt.Errorf("recaptcha: 出现人机验证")

// errManualCheck is a sentinel error indicating the account needs manual review.
var errManualCheck = fmt.Errorf("manual_check: 需要人工确认")

// checkRecaptcha checks if the current URL is a recaptcha challenge page.
func checkRecaptcha(pageURL string) bool {
	return strings.Contains(pageURL, "signin/challenge/recaptcha")
}

func updateStatus(batchID string, idx int, status db.SubAccountStatus, step string, errMsg string) {
	db.DB.UpdateSubAccount(batchID, idx, status, step, errMsg)
}

// runSingleAttempt runs the hardcoded OAuth flow for one account.
// pw is the shared playwright instance.
func runSingleAttempt(pw *playwright.Playwright, account db.SubAccount, batchID string, idx int) error {
	email := account.Email

	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false), // always false — we use --headless=new arg instead
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--disable-infobars",
			"--disable-extensions",
		},
	}

	// New headless mode: behaves like headed browser, undetectable by Google
	if config.Get().Headless {
		launchOpts.Args = append(launchOpts.Args, "--headless=new")
	}

	if cfg := config.Get(); cfg.ProxyEnabled && cfg.Proxy != "" {
		launchOpts.Proxy = &playwright.Proxy{Server: cfg.Proxy}
	}

	browser, err := pw.Chromium.Launch(launchOpts)
	if err != nil {
		return fmt.Errorf("could not launch browser: %v", err)
	}
	defer browser.Close()

	bctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
	})
	if err != nil {
		return fmt.Errorf("could not create context: %v", err)
	}
	defer bctx.Close()

	// Block unnecessary resources in headless mode for faster loading
	if config.Get().Headless {
		// Domains to block entirely (fonts, analytics, tracking, ads)
		blockedDomains := []string{
			"fonts.gstatic.com",
			"fonts.googleapis.com",
			"www.google-analytics.com",
			"www.googletagmanager.com",
			"ssl.gstatic.com/accounts/o/",
			"play.google.com/log",
			"ogs.google.com",
		}
		bctx.Route("**/*", func(route playwright.Route) {
			url := route.Request().URL()
			// Block by resource type
			switch route.Request().ResourceType() {
			case "image", "font", "media":
				route.Abort()
				return
			}
			// Block by domain
			for _, d := range blockedDomains {
				if strings.Contains(url, d) {
					route.Abort()
					return
				}
			}
			route.Continue()
		})
	}

	page, err := bctx.NewPage()
	if err != nil {
		return fmt.Errorf("could not create page: %v", err)
	}

	// Intercept OAuth callback — extract code from redirect URL and complete flow locally
	oauthCallbackCh := make(chan bool, 1)
	bctx.OnRequest(func(req playwright.Request) {
		reqURL := req.URL()
		if strings.HasPrefix(reqURL, "http://localhost:51121/oauth-callback") {
			parsed, parseErr := url.Parse(reqURL)
			if parseErr != nil {
				L.Fail(email, fmt.Sprintf("OAuth Callback URL 解析失败: %v", parseErr))
				select { case oauthCallbackCh <- false: default: }
				return
			}
			code := parsed.Query().Get("code")
			if code == "" {
				errMsg := parsed.Query().Get("error")
				L.Fail(email, fmt.Sprintf("OAuth Callback 无 code: error=%s", errMsg))
				select { case oauthCallbackCh <- false: default: }
				return
			}
			// Complete OAuth flow locally: exchange code → fetch email → fetch project → save auth file
			if _, cbErr := api.CompleteOAuthFlow(code); cbErr != nil {
				L.Fail(email, fmt.Sprintf("OAuth 本地流程失败: %v", cbErr))
				select { case oauthCallbackCh <- false: default: }
			} else {
				L.OK(email, "OAuth 本地流程完成, 凭证已保存")
				select { case oauthCallbackCh <- true: default: }
			}
		}
	})

	updateStatus(batchID, idx, db.StatusRunning, "login", "")

	// ─── Step 1: Login to Gmail ───
	L.Step(email, "开始登录...")
	if err := doLogin(email, account, page); err != nil {
		return err
	}

	// ─── Step 2: OAuth → CPA check → (phone bind / retry) loop ───
	const maxCPACycles = 2    // total OAuth→CPA cycles
	const maxCPAPolls = 10    // polls per cycle (10 × 3s = 30s max wait)
	const cpaPollInterval = 3 // seconds between polls
	const maxOAuthRetries = 3 // retries if callback fails

	// Check if phone is already bound — if so, just do one OAuth and done
	phoneBound := false
	if b := db.DB.GetBatch(batchID); b != nil && idx < len(b.Accounts) {
		phoneBound = b.Accounts[idx].PhoneBound
	}

	// OAuth with callback-failure retry
	doOAuthWithRetry := func(label string) error {
		for oauthRetry := 1; oauthRetry <= maxOAuthRetries; oauthRetry++ {
			updateStatus(batchID, idx, db.StatusRunning, "oauth", "")
			if oauthRetry > 1 {
				L.Warn(email, fmt.Sprintf("%s 第 %d/%d 次重试...", label, oauthRetry, maxOAuthRetries))
				time.Sleep(5 * time.Second)
			} else {
				L.Step(email, label)
			}
			err := doOAuthStart(email, account, bctx, oauthCallbackCh)
			if err == nil {
				return nil
			}
			L.Warn(email, fmt.Sprintf("%s 失败: %v", label, err))
			if oauthRetry == maxOAuthRetries {
				return fmt.Errorf("%s failed after %d retries: %w", label, maxOAuthRetries, err)
			}
		}
		return nil
	}

	// First OAuth
	if err := doOAuthWithRetry("OAuth 授权"); err != nil {
		return err
	}

	// If phone already bound, we're done — no need for CPA check
	if phoneBound {
		L.OK(email, "手机已绑定, 无需 CPA 检测, 完成")
		return nil
	}

	for cycle := 0; cycle < maxCPACycles; cycle++ {
		updateStatus(batchID, idx, db.StatusRunning, "check_cpa", "")
		L.Step(email, fmt.Sprintf("检查 CPA 状态 (第 %d/%d 轮)...", cycle+1, maxCPACycles))

		var validationURL string

		for poll := 0; poll < maxCPAPolls; poll++ {
			if poll > 0 {
				time.Sleep(time.Duration(cpaPollInterval) * time.Second)
			}

			status, url, err := api.CheckCPAOnce(email)
			if err != nil {
				L.Warn(email, fmt.Sprintf("CPA 查询失败 (%d/%d): %v", poll+1, maxCPAPolls, err))
				continue
			}

			switch status {
			case api.CPAHasURL:
				validationURL = url
				L.Warn(email, fmt.Sprintf("需要手机绑定, URL: %s", url))

			case api.CPAActive:
				L.Info(email, fmt.Sprintf("CPA 状态: active (%d/%d)", poll+1, maxCPAPolls))
				continue

			case api.CPAErrorNoURL:
				L.Info(email, fmt.Sprintf("CPA 有错误但无绑定链接, 继续轮询 (%d/%d)", poll+1, maxCPAPolls))
				continue

			case api.CPANotFound:
				L.Info(email, fmt.Sprintf("CPA 未找到账号, 等待中 (%d/%d)", poll+1, maxCPAPolls))
				continue
			}

			break // CPAHasURL found
		}

		if validationURL != "" {
			// ─── Phone binding needed ───
			updateStatus(batchID, idx, db.StatusRunning, "phone_bind", "")
			if err := doPhoneBind(email, page, validationURL, batchID, idx); err != nil {
				return fmt.Errorf("phone_bind failed: %w", err)
			}

			// Phone bind succeeded → delete CPA → final re-OAuth
			updateStatus(batchID, idx, db.StatusRunning, "delete_cpa", "")
			L.Info(email, "手机绑定完成, 删除 CPA 凭证...")
			if err := api.DeleteAuthFile(email); err != nil {
				L.Warn(email, fmt.Sprintf("删除 CPA 凭证失败: %v", err))
			}
			time.Sleep(3 * time.Second)

			if err := doOAuthWithRetry("重新 OAuth 授权 (最终)"); err != nil {
				return err
			}
			L.OK(email, "手机绑定 + 重新授权完成")
			return nil
		} // end if validationURL != ""

		// ─── Active but no result after all polls → delete CPA, re-OAuth ───
		if cycle < maxCPACycles-1 {
			updateStatus(batchID, idx, db.StatusRunning, "delete_cpa", "")
			L.Info(email, fmt.Sprintf("CPA 轮询 %d 次仍为 active, 删除凭证, 重新 OAuth...", maxCPAPolls))
			if err := api.DeleteAuthFile(email); err != nil {
				L.Warn(email, fmt.Sprintf("删除 CPA 凭证失败: %v", err))
			}
			time.Sleep(3 * time.Second)

			if err := doOAuthWithRetry(fmt.Sprintf("重新 OAuth 授权 (第 %d 轮)", cycle+2)); err != nil {
				return err
			}
		}
	}

	// All cycles exhausted — mark for manual review and skip
	L.Warn(email, fmt.Sprintf("CPA 验证 %d 轮均未获取到手机绑定链接, 标记为人工确认", maxCPACycles))
	return errManualCheck
}

// ─── Login Logic ────────────────────────────────────────────────

func doLogin(email string, account db.SubAccount, page playwright.Page) error {
	// Navigate to Gmail
	L.Info(email, "打开 Gmail...")
	if _, err := page.Goto("https://mail.google.com", playwright.PageGotoOptions{
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return fmt.Errorf("login: navigate to gmail failed: %w", err)
	}
	time.Sleep(2 * time.Second)

	// Input email
	L.Info(email, "输入邮箱...")
	if err := page.Locator(`input[type="email"]`).Fill(email); err != nil {
		return fmt.Errorf("login: fill email failed: %w", err)
	}

	// Click next
	if err := page.Locator("#identifierNext button").Click(); err != nil {
		return fmt.Errorf("login: click email next failed: %w", err)
	}
	time.Sleep(2 * time.Second)

	// Input password (use name="Passwd" to avoid hidden input conflict)
	L.Info(email, "输入密码...")
	pwInput := page.Locator(`input[name="Passwd"]`)
	if err := pwInput.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("login: wait for password input failed: %w", err)
	}
	if err := pwInput.Fill(account.Password); err != nil {
		return fmt.Errorf("login: fill password failed: %w", err)
	}

	// Click next
	if err := page.Locator("#passwordNext button").Click(); err != nil {
		return fmt.Errorf("login: click password next failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Post-login: detect challenges and handle them in a loop
	for round := 0; round < 10; round++ {
		currentURL := page.URL()

		// Success: reached Gmail inbox
		if strings.Contains(currentURL, "mail.google.com/mail/u/0") {
			L.OK(email, "登录成功")
			return nil
		}

		// Recaptcha: abort immediately
		if checkRecaptcha(currentURL) {
			L.Fail(email, "出现人机验证，跳过该账号")
			return errRecaptcha
		}

		// Challenge selection
		if strings.Contains(currentURL, "challenge/selection") {
			L.Info(email, "检测到验证方式选择...")
			// Prefer TOTP (type 6), then aux email (type 12)
			totpOpt := page.Locator(`div[data-challengetype="6"]`)
			if cnt, _ := totpOpt.Count(); cnt > 0 {
				_ = totpOpt.First().Click()
				time.Sleep(3 * time.Second)
				continue
			}
			auxOpt := page.Locator(`div[data-challengetype="12"]`)
			if cnt, _ := auxOpt.Count(); cnt > 0 {
				_ = auxOpt.First().Click()
				time.Sleep(3 * time.Second)
				continue
			}
			return fmt.Errorf("login: unrecognized verification methods on challenge/selection")
		}

		// TOTP challenge
		if strings.Contains(currentURL, "challenge/totp") {
			L.Info(email, "处理 TOTP 验证...")
			if account.TwoFA == "" {
				return fmt.Errorf("login: TOTP required but no 2FA secret")
			}
			code, err := totp.GenerateCode(account.TwoFA, time.Now())
			if err != nil {
				return fmt.Errorf("login: generate TOTP failed: %w", err)
			}
			if err := page.Locator(`input[name="totpPin"]`).Fill(code); err != nil {
				return fmt.Errorf("login: fill TOTP failed: %w", err)
			}
			if err := page.Locator("#totpNext").Click(); err != nil {
				return fmt.Errorf("login: click TOTP next failed: %w", err)
			}
			// Wait for URL to change before re-entering loop
			for j := 0; j < 15; j++ {
				time.Sleep(2 * time.Second)
				if page.URL() != currentURL {
					break
				}
			}
			continue
		}

		// Auxiliary email challenge
		if strings.Contains(currentURL, "challenge/kpe") || strings.Contains(currentURL, "challenge/knowledge") {
			L.Info(email, "处理辅助邮箱验证...")
			if account.AuxEmail == "" {
				return fmt.Errorf("login: aux email required but not configured")
			}
			inputLoc := page.Locator(`input[type="email"]`)
			if cnt, _ := inputLoc.Count(); cnt == 0 {
				inputLoc = page.Locator(`input[name="knowledgePreregisteredEmailResponse"]`)
			}
			if err := inputLoc.First().Fill(account.AuxEmail); err != nil {
				return fmt.Errorf("login: fill aux email failed: %w", err)
			}
			nextBtn := page.Locator(`button[jsname="LgbsSe"]`)
			if err := nextBtn.Last().Click(); err != nil {
				return fmt.Errorf("login: click aux email next failed: %w", err)
			}
			// Wait for URL to change
			for j := 0; j < 15; j++ {
				time.Sleep(2 * time.Second)
				if page.URL() != currentURL {
					break
				}
			}
			continue
		}

		// Recovery options page - skip
		if strings.Contains(currentURL, "recoveryoptions") {
			L.Info(email, "跳过恢复选项页面...")
			skipBtn := page.Locator(`button[jsname="Hx0NGb"]`)
			if cnt, _ := skipBtn.Count(); cnt > 0 {
				_ = skipBtn.First().Click()
			} else {
				notNowBtn := page.Locator(`button[jsname="LgbsSe"]`)
				_ = notNowBtn.Last().Click()
			}
			time.Sleep(3 * time.Second)
			continue
		}

		// Home address page - skip
		if strings.Contains(currentURL, "homeaddress") {
			L.Info(email, "跳过家庭地址页面...")
			skipBtn := page.Locator(`button[jsname="Hx0NGb"]`)
			if cnt, _ := skipBtn.Count(); cnt > 0 {
				_ = skipBtn.First().Click()
			}
			time.Sleep(3 * time.Second)
			continue
		}

		// Phone uplevel step selection - skip
		if strings.Contains(currentURL, "uplevelingstep/selection") {
			L.Info(email, "跳过手机号绑定建议...")
			skipBtn := page.Locator(`button[jsname="Hx0NGb"]`)
			if cnt, _ := skipBtn.Count(); cnt > 0 {
				_ = skipBtn.First().Click()
			}
			time.Sleep(3 * time.Second)
			continue
		}

		// Phone IAP challenge - skip
		if strings.Contains(currentURL, "challenge/iap") {
			L.Info(email, "跳过手机号验证...")
			skipBtn := page.Locator(`button[jsname="Hx0NGb"]`)
			if cnt, _ := skipBtn.Count(); cnt > 0 {
				_ = skipBtn.First().Click()
			}
			time.Sleep(3 * time.Second)
			continue
		}

		// Wait and re-check
		time.Sleep(3 * time.Second)
	}

	// Final check
	if strings.Contains(page.URL(), "mail.google.com/mail/u/0") {
		L.OK(email, "登录成功")
		return nil
	}

	return fmt.Errorf("login: failed to reach Gmail inbox after login, current URL: %s", page.URL())
}

// ─── OAuth Start Logic ──────────────────────────────────────────

// doOAuthStart runs the full OAuth flow locally (no CPA API dependency, no concurrency lock).
// Generates URL → browser consent → intercept callback code → exchange token → save auth file.
func doOAuthStart(email string, account db.SubAccount, bctx playwright.BrowserContext, oauthCallbackCh chan bool) error {
	// Generate state and build URL locally
	state := api.GenerateOAuthState()
	oauthURL := api.BuildOAuthURL(state)
	L.Info(email, fmt.Sprintf("生成 OAuth URL (state=%s...)", state[:8]))

	oauthPage, err := bctx.NewPage()
	if err != nil {
		return fmt.Errorf("oauth_start: create new page failed: %w", err)
	}
	defer func() {
		if err := oauthPage.Close(); err != nil {
			L.Warn(email, fmt.Sprintf("关闭 OAuth 页面失败: %v", err))
		}
	}()

	if _, err := oauthPage.Goto(oauthURL, playwright.PageGotoOptions{
		Timeout: playwright.Float(60000),
	}); err != nil {
		return fmt.Errorf("oauth_start: navigate to oauth url failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Handle account chooser
	currentURL := oauthPage.URL()
	if strings.Contains(currentURL, "accountchooser") {
		L.Info(email, "检测到账号选择器，点击第一个账号...")
		chooser := oauthPage.Locator(`div[data-button-type="multipleChoiceIdentifier"]`).First()
		if err := chooser.Click(); err != nil {
			L.Warn(email, fmt.Sprintf("点击账号选择器失败: %v", err))
		}
		time.Sleep(3 * time.Second)
	}

	// Poll URL for up to 240s
	consentReached := false
	for i := 0; i < 120; i++ {
		currentURL = oauthPage.URL()

		// Consent page
		if strings.Contains(currentURL, "signin/oauth/firstparty/nativeapp") ||
			strings.Contains(currentURL, "signin/oauth/consent") {
			L.OK(email, "已到达 OAuth 同意页面")
			consentReached = true
			break
		}

		// Recaptcha during OAuth
		if checkRecaptcha(currentURL) {
			L.Fail(email, "OAuth 过程出现人机验证，跳过该账号")
			return errRecaptcha
		}

		// Callback already happened
		if strings.Contains(currentURL, "localhost:51121/oauth-callback") {
			L.OK(email, "OAuth 回调已触发")
			select {
			case ok := <-oauthCallbackCh:
				if ok {
					L.OK(email, "OAuth 回调确认成功")
					return nil
				}
				return fmt.Errorf("oauth_start: callback submission failed")
			case <-time.After(30 * time.Second):
				return fmt.Errorf("oauth_start: oauth callback confirmation timeout")
			}
		}

		// TOTP challenge
		if strings.Contains(currentURL, "challenge/totp") {
			L.Info(email, "检测到 TOTP 验证...")
			if account.TwoFA == "" {
				return fmt.Errorf("oauth_start: TOTP required but no 2FA secret configured")
			}
			code, err := totp.GenerateCode(account.TwoFA, time.Now())
			if err != nil {
				return fmt.Errorf("oauth_start: generate TOTP code failed: %w", err)
			}
			L.Info(email, fmt.Sprintf("生成 TOTP 验证码: %s", code))
			if err := oauthPage.Locator(`input[name="totpPin"]`).Fill(code); err != nil {
				return fmt.Errorf("oauth_start: fill TOTP code failed: %w", err)
			}
			if err := oauthPage.Locator("#totpNext").Click(); err != nil {
				return fmt.Errorf("oauth_start: click TOTP next failed: %w", err)
			}
			for j := 0; j < 15; j++ {
				time.Sleep(2 * time.Second)
				if oauthPage.URL() != currentURL {
					break
				}
			}
			continue
		}

		// Challenge selection page
		if strings.Contains(currentURL, "challenge/selection") {
			L.Info(email, "检测到验证方式选择页面...")
			totpOption := oauthPage.Locator(`div[data-challengetype="6"]`)
			totpCount, _ := totpOption.Count()
			if totpCount > 0 {
				L.Info(email, "选择 TOTP 验证方式")
				_ = totpOption.First().Click()
				time.Sleep(3 * time.Second)
				continue
			}
			auxOption := oauthPage.Locator(`div[data-challengetype="12"]`)
			auxCount, _ := auxOption.Count()
			if auxCount > 0 {
				L.Info(email, "选择辅助邮箱验证方式")
				_ = auxOption.First().Click()
				time.Sleep(3 * time.Second)
				continue
			}
			L.Warn(email, "未找到可用的验证方式")
		}

		// Auxiliary email / knowledge challenge
		if strings.Contains(currentURL, "challenge/kpe") || strings.Contains(currentURL, "challenge/knowledge") {
			L.Info(email, "检测到辅助邮箱验证...")
			if account.AuxEmail == "" {
				return fmt.Errorf("oauth_start: aux email challenge but no aux_email configured")
			}
			inputLoc := oauthPage.Locator(`input[type="email"]`)
			count, _ := inputLoc.Count()
			if count == 0 {
				inputLoc = oauthPage.Locator(`input[name="knowledgePreregisteredEmailResponse"]`)
			}
			if err := inputLoc.First().Fill(account.AuxEmail); err != nil {
				L.Warn(email, fmt.Sprintf("填写辅助邮箱失败: %v", err))
			}
			nextBtn := oauthPage.Locator(`button[jsname="LgbsSe"]`)
			if err := nextBtn.Last().Click(); err != nil {
				L.Warn(email, fmt.Sprintf("点击下一步失败: %v", err))
			}
			time.Sleep(3 * time.Second)
			continue
		}

		time.Sleep(2 * time.Second)
	}

	if !consentReached {
		return fmt.Errorf("oauth_start: consent page not reached after 240s")
	}

	// Click Allow button
	L.Info(email, "点击允许按钮...")
	allowBtn := oauthPage.Locator(`button[jsname="LgbsSe"]`)
	if err := allowBtn.Last().Click(); err != nil {
		return fmt.Errorf("oauth_start: click allow button failed: %w", err)
	}
	time.Sleep(5 * time.Second)

	// Wait for callback
	L.Info(email, "等待 OAuth 回调确认...")
	select {
	case ok := <-oauthCallbackCh:
		if ok {
			L.OK(email, "OAuth 授权完成")
			return nil
		}
		return fmt.Errorf("oauth_start: callback submission failed")
	case <-time.After(30 * time.Second):
		return fmt.Errorf("oauth_start: oauth callback confirmation timeout")
	}
}

// ─── Phone Bind Logic ───────────────────────────────────────────

func doPhoneBind(email string, page playwright.Page, validationURL string, batchID string, idx int) error {
	const maxRetries = 3

	L.Info(email, fmt.Sprintf("开始手机绑定流程 (最多 %d 次尝试)", maxRetries))

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			L.Info(email, fmt.Sprintf("手机绑定重试 %d/%d", attempt+1, maxRetries))
		}

		if _, err := page.Goto(validationURL, playwright.PageGotoOptions{
			Timeout: playwright.Float(60000),
		}); err != nil {
			L.Warn(email, fmt.Sprintf("导航到验证页面失败: %v", err))
			continue
		}
		time.Sleep(5 * time.Second)

		success := false
		for i := 0; i < 30; i++ {
			currentURL := page.URL()

			if strings.Contains(currentURL, "gemini-code-assist/auth/auth_success") ||
				strings.Contains(currentURL, "mail.google.com/mail/u/0") {
				L.OK(email, "手机绑定成功")
				db.DB.SetPhoneBound(batchID, idx)
				return nil
			}

			if strings.Contains(currentURL, "chrome-error") {
				return fmt.Errorf("phone_bind: chrome error encountered")
			}

			if strings.Contains(currentURL, "/uplevelingstep/selection") {
				L.Info(email, "检测到验证步骤选择页面, 点击短信验证选项...")
				stepOption := page.Locator(`div[data-step-type="1"]`).First()
				if err := stepOption.Click(); err != nil {
					L.Warn(email, fmt.Sprintf("点击验证步骤失败: %v", err))
				}
				waited := false
				for j := 0; j < 20; j++ {
					time.Sleep(2 * time.Second)
					newURL := page.URL()
					if !strings.Contains(newURL, "/uplevelingstep/selection") {
						L.OK(email, fmt.Sprintf("已离开选择页面, 当前: %s", newURL))
						waited = true
						break
					}
				}
				if !waited {
					L.Warn(email, "等待离开选择页面超时, 继续轮询")
				}
				continue
			}

			if strings.Contains(currentURL, "/challenge/iap") {
				L.Info(email, "检测到手机号输入页面...")
				if err := handlePhoneInput(email, page); err != nil {
					L.Warn(email, fmt.Sprintf("手机号输入处理失败: %v", err))
					break
				}
				success = true
				break
			}

			time.Sleep(3 * time.Second)
		}

		if success {
			for i := 0; i < 30; i++ {
				currentURL := page.URL()
				if strings.Contains(currentURL, "gemini-code-assist/auth/auth_success") ||
					strings.Contains(currentURL, "mail.google.com/mail/u/0") {
					L.OK(email, "手机绑定成功")
					db.DB.SetPhoneBound(batchID, idx)
					return nil
				}
				time.Sleep(3 * time.Second)
			}
		}
	}

	return fmt.Errorf("phone_bind: failed after %d retries", maxRetries)
}

func handlePhoneInput(email string, page playwright.Page) error {
	L.Info(email, "获取手机号码...")
	formattedPhone, rawPhone, phoneID, err := api.GetPhoneNumber()
	if err != nil {
		return fmt.Errorf("get phone number failed: %w", err)
	}
	L.Info(email, fmt.Sprintf("获取到手机号: %s", formattedPhone))

	phoneInput := page.Locator(`input[type="tel"]`).First()
	if err := phoneInput.Fill(formattedPhone); err != nil {
		return fmt.Errorf("fill phone number failed: %w", err)
	}

	nextBtn := page.Locator(`button[jsname="LgbsSe"]`)
	if err := nextBtn.Last().Click(); err != nil {
		return fmt.Errorf("click send SMS button failed: %w", err)
	}
	time.Sleep(5 * time.Second)

	L.Info(email, "等待短信验证码...")
	smsCode, err := api.GetSMSCode(rawPhone, phoneID)
	if err != nil {
		return fmt.Errorf("get SMS code failed: %w", err)
	}
	L.Info(email, fmt.Sprintf("获取到验证码: %s", smsCode))

	codeInput := page.Locator(`input[type="tel"]`).First()
	if err := codeInput.Fill(smsCode); err != nil {
		return fmt.Errorf("fill SMS code failed: %w", err)
	}

	verifyBtn := page.Locator(`button[jsname="LgbsSe"]`)
	if err := verifyBtn.Last().Click(); err != nil {
		return fmt.Errorf("click verify button failed: %w", err)
	}
	time.Sleep(5 * time.Second)

	return nil
}

// ─── Runner ─────────────────────────────────────────────────────

func runAutomationForAccount(pw *playwright.Playwright, account db.SubAccount, batchID string, idx int) {
	// Recover from panics (browser crash etc.) to avoid killing the whole batch
	defer func() {
		if r := recover(); r != nil {
			errMsg := fmt.Sprintf("panic: %v", r)
			L.Fail(account.Email, errMsg)
			updateStatus(batchID, idx, db.StatusError, "panic", errMsg)
		}
	}()

	email := account.Email
	updateStatus(batchID, idx, db.StatusRunning, "starting", "")

	for attempt := 1; attempt <= db.MaxRetries; attempt++ {
		if attempt > 1 {
			L.Warn(email, fmt.Sprintf("第 %d/%d 次重试", attempt, db.MaxRetries))
		}

		err := runSingleAttempt(pw, account, batchID, idx)
		if err == nil {
			updateStatus(batchID, idx, db.StatusSuccess, "done", "")
			L.OK(email, "全部完成")
			return
		}

		// Recaptcha: mark as error and skip, no retry
		if err == errRecaptcha {
			updateStatus(batchID, idx, db.StatusError, "recaptcha", "出现人机验证")
			return
		}

		// Manual check: mark and skip, no retry
		if err == errManualCheck {
			updateStatus(batchID, idx, db.StatusError, "manual_check", "CPA 未返回手机绑定链接, 等待人工绑定")
			return
		}

		errMsg := fmt.Sprintf("第 %d/%d 次: %v", attempt, db.MaxRetries, err)
		L.Fail(email, errMsg)

		if attempt < db.MaxRetries {
			updateStatus(batchID, idx, db.StatusRunning, "retry_wait", errMsg)
			time.Sleep(10 * time.Second)
		} else {
			updateStatus(batchID, idx, db.StatusFailed, "exhausted", errMsg)
		}
	}
}

// ─── Global concurrency limiter ──────────────────────────────────

var (
	globalSem     chan struct{}
	globalSemMu   sync.Mutex
	globalSemSize int
)

// getGlobalSem returns the global concurrency semaphore.
// Recreates it if the config concurrency value changed.
func getGlobalSem() chan struct{} {
	globalSemMu.Lock()
	defer globalSemMu.Unlock()

	concurrency := config.Get().Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if globalSem == nil || globalSemSize != concurrency {
		globalSem = make(chan struct{}, concurrency)
		globalSemSize = concurrency
	}
	return globalSem
}

// RunBatch processes all accounts in a batch using the global concurrency limiter.
func RunBatch(batchID string) {
	batch := db.DB.GetBatch(batchID)
	if batch == nil {
		L.Sys(fmt.Sprintf("批次 %s 未找到", batchID))
		return
	}

	db.DB.UpdateBatchStatus(batchID, db.BatchRunning)

	sem := getGlobalSem()

	L.Banner(fmt.Sprintf("批次启动: %d 个子号, 全局并发上限 %d", len(batch.Accounts), cap(sem)))

	if err := playwright.Install(); err != nil {
		L.Sys(fmt.Sprintf("Playwright 安装失败: %v", err))
		db.DB.UpdateBatchStatus(batchID, db.BatchFinished)
		return
	}

	pw, err := playwright.Run()
	if err != nil {
		L.Sys(fmt.Sprintf("Playwright 启动失败: %v", err))
		db.DB.UpdateBatchStatus(batchID, db.BatchFinished)
		return
	}
	defer pw.Stop()

	var wg sync.WaitGroup

	for i, acc := range batch.Accounts {
		if acc.Status == db.StatusSuccess {
			L.Info(acc.Email, "已成功, 跳过")
			continue
		}

		wg.Add(1)
		sem <- struct{}{} // acquire global slot

		go func(idx int, account db.SubAccount) {
			defer wg.Done()
			defer func() { <-sem }() // release global slot

			L.Banner(fmt.Sprintf("开始处理 %d/%d: %s", idx+1, len(batch.Accounts), account.Email))
			runAutomationForAccount(pw, account, batchID, idx)
		}(i, acc)
	}

	wg.Wait()

	db.DB.UpdateBatchStatus(batchID, db.BatchFinished)
	L.Banner("批次执行完毕")
}
