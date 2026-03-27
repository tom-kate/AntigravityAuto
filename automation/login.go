package automation

import (
	"fmt"
	"strings"
	"time"

	"antiauto/db"
	L "antiauto/logger"

	"github.com/pquerna/otp/totp"
	"github.com/playwright-community/playwright-go"
)

// doLogin handles the Gmail login flow including TOTP, auxiliary email, and various challenge pages.
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
