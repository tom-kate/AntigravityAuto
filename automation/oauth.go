package automation

import (
	"fmt"
	"strings"
	"time"

	"antiauto/api"
	"antiauto/db"
	L "antiauto/logger"

	"github.com/pquerna/otp/totp"
	"github.com/playwright-community/playwright-go"
)

// doOAuthStart runs the full OAuth flow locally (no CPA API dependency, no concurrency lock).
// Generates URL → browser consent → intercept callback code → exchange token → save auth file.
func doOAuthStart(email string, account db.SubAccount, bctx playwright.BrowserContext, oauthCallbackCh chan error) error {
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
			case cbErr := <-oauthCallbackCh:
				if cbErr == nil {
					L.OK(email, "OAuth 回调确认成功")
					return nil
				}
				return cbErr
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
	case cbErr := <-oauthCallbackCh:
		if cbErr == nil {
			L.OK(email, "OAuth 授权完成")
			return nil
		}
		return cbErr
	case <-time.After(30 * time.Second):
		return fmt.Errorf("oauth_start: oauth callback confirmation timeout")
	}
}
