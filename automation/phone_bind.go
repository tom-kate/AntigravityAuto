package automation

import (
	"fmt"
	"strings"
	"time"

	"antiauto/api"
	"antiauto/db"
	L "antiauto/logger"

	"github.com/playwright-community/playwright-go"
)

// doPhoneBind handles the phone binding flow after navigating to the validation URL.
func doPhoneBind(email string, page playwright.Page, validationURL string, batchID string, idx int) error {
	L.Info(email, "开始手机绑定流程")

	if _, err := page.Goto(validationURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(30000),
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("phone_bind: navigate failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	enteredPhoneFlow := false
	for i := 0; i < 30; i++ {
		currentURL := page.URL()

		if strings.Contains(currentURL, "gemini-code-assist/auth/auth_success") ||
			strings.Contains(currentURL, "mail.google.com/mail/u/0") {
			if !enteredPhoneFlow {
				L.Warn(email, "打开验证链接直接跳转到成功页, 无需手机绑定, 标记为需要重启")
				return errNeedRestart
			}
			L.OK(email, "手机绑定成功")
			db.DB.SetPhoneBound(batchID, idx)
			return nil
		}

		if strings.Contains(currentURL, "chrome-error") {
			return fmt.Errorf("phone_bind: chrome error")
		}

		if strings.Contains(currentURL, "/uplevelingstep/selection") {
			enteredPhoneFlow = true
			L.Info(email, "检测到验证步骤选择页面, 点击短信验证选项...")
			stepOption := page.Locator(`div[data-step-type="1"]`).First()
			if err := stepOption.Click(); err != nil {
				L.Warn(email, fmt.Sprintf("点击验证步骤失败: %v", err))
			}
			for j := 0; j < 20; j++ {
				time.Sleep(2 * time.Second)
				if !strings.Contains(page.URL(), "/uplevelingstep/selection") {
					L.OK(email, "已离开选择页面")
					break
				}
			}
			continue
		}

		if strings.Contains(currentURL, "/challenge/iap") {
			enteredPhoneFlow = true
			L.Info(email, "检测到手机号输入页面...")
			if err := handlePhoneInput(email, page, validationURL); err != nil {
				return fmt.Errorf("phone_bind: %w", err)
			}
			// Wait for success redirect
			for k := 0; k < 30; k++ {
				cur := page.URL()
				if strings.Contains(cur, "gemini-code-assist/auth/auth_success") ||
					strings.Contains(cur, "mail.google.com/mail/u/0") {
					L.OK(email, "手机绑定成功")
					db.DB.SetPhoneBound(batchID, idx)
					return nil
				}
				time.Sleep(3 * time.Second)
			}
			return fmt.Errorf("phone_bind: 手机验证后未跳转到成功页")
		}

		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("phone_bind: 未检测到手机绑定页面")
}

// handlePhoneInput gets a phone number via HeroSMS, enters it, receives SMS code, and verifies.
// Retries up to 3 times with new numbers on failure.
func handlePhoneInput(email string, page playwright.Page, validationURL string) error {
	const maxAttempts = 3

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			L.Warn(email, fmt.Sprintf("第 %d 次重试获取手机号, 重新导航到验证页...", attempt))
			if _, err := page.Goto(validationURL, playwright.PageGotoOptions{
				Timeout:   playwright.Float(30000),
				WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			}); err != nil {
				L.Warn(email, fmt.Sprintf("重新导航失败: %v", err))
				continue
			}
			time.Sleep(3 * time.Second)

			// Wait for /challenge/iap page again
			foundIAP := false
			for w := 0; w < 10; w++ {
				curURL := page.URL()
				if strings.Contains(curURL, "/challenge/iap") {
					foundIAP = true
					break
				}
				if strings.Contains(curURL, "/uplevelingstep/selection") {
					stepOption := page.Locator(`div[data-step-type="1"]`).First()
					stepOption.Click()
				}
				time.Sleep(2 * time.Second)
			}
			if !foundIAP {
				L.Warn(email, "重新导航后未到达手机号输入页")
				continue
			}
		}

		L.Info(email, "获取手机号码...")
		formattedPhone, activationID, err := api.GetPhoneNumber()
		if err != nil {
			L.Warn(email, fmt.Sprintf("获取手机号失败: %v", err))
			continue
		}
		L.Info(email, fmt.Sprintf("获取到手机号: %s (activation: %s)", formattedPhone, activationID))

		phoneInput := page.Locator(`input[type="tel"]`).First()
		if err := phoneInput.Fill(formattedPhone); err != nil {
			api.ReleasePhone(activationID)
			L.Warn(email, fmt.Sprintf("填入手机号失败: %v", err))
			continue
		}

		nextBtn := page.Locator(`button[jsname="LgbsSe"]`)
		if err := nextBtn.Last().Click(); err != nil {
			api.ReleasePhone(activationID)
			L.Warn(email, fmt.Sprintf("点击发送短信按钮失败: %v", err))
			continue
		}
		time.Sleep(5 * time.Second)

		L.Info(email, "等待短信验证码...")
		smsCode, err := api.GetSMSCode(activationID)
		if err != nil {
			L.Warn(email, fmt.Sprintf("获取验证码失败: %v, 释放手机号并重试", err))
			api.ReleasePhone(activationID)
			continue
		}
		L.Info(email, fmt.Sprintf("获取到验证码: %s", smsCode))

		// Complete the activation on SMS platform
		api.SetActivationStatus(activationID, 6)

		codeInput := page.Locator(`input[type="tel"]`).First()
		if err := codeInput.Fill(smsCode); err != nil {
			return fmt.Errorf("fill SMS code failed: %w", err)
		}

		verifyBtn := page.Locator(`button[jsname="LgbsSe"]`)
		if err := verifyBtn.Last().Click(); err != nil {
			return fmt.Errorf("click verify button failed: %w", err)
		}
		time.Sleep(5 * time.Second)

		return nil // success
	}

	return fmt.Errorf("手机号验证失败: %d 次尝试均未成功", maxAttempts)
}
