package automation

import (
	"fmt"
	"strings"
	"time"

	"antiauto/config"
	L "antiauto/logger"

	"github.com/playwright-community/playwright-go"
)

// iframeSelector 是支付 iframe 的选择器
const iframeSelector = `iframe[title="Complete your purchase"]`

// doAgeVerify 通过信用卡完成 Google 年龄验证。
// 打开 credit-card 页面，切换国家为美国，填表提交。
func doAgeVerify(email string, page playwright.Page) error {
	cfg := config.Get()
	if cfg.CardNumber == "" || cfg.CardExpiry == "" || cfg.CardCVV == "" {
		L.Fail(email, "年龄验证: 信用卡信息未配置")
		return errAgeVerification
	}

	// 新标签页打开信用卡验证页面
	L.Info(email, "打开信用卡年龄验证页面...")
	agePage, err := page.Context().NewPage()
	if err != nil {
		return fmt.Errorf("age_verify: 创建新标签页失败: %w", err)
	}
	defer agePage.Close()

	if _, err := agePage.Goto("https://myaccount.google.com/age-verification/credit-card", playwright.PageGotoOptions{
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return fmt.Errorf("age_verify: 导航失败: %w", err)
	}
	if err := agePage.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return fmt.Errorf("age_verify: 等待加载失败: %w", err)
	}
	time.Sleep(3 * time.Second)

	fl := agePage.FrameLocator(iframeSelector)

	// 等待 iframe 内表单加载
	L.Info(email, "等待支付 iframe 加载...")
	if err := waitForIframeReady(email, fl); err != nil {
		return err
	}

	// 填写信用卡表单
	if err := fillCardForm(email, fl, cfg); err != nil {
		return err
	}

	// 点击提交
	if err := clickSubmit(email, fl); err != nil {
		return err
	}

	// 等待验证结果
	return waitForResult(email, agePage, fl)
}

// waitForIframeReady 等待 iframe 内表单元素可用。
func waitForIframeReady(email string, fl playwright.FrameLocator) error {
	cardInput := fl.Locator(`input[inputmode="numeric"]`).First()
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		if cnt, _ := cardInput.Count(); cnt > 0 {
			L.Info(email, "支付 iframe 表单已加载")
			return nil
		}
		if i > 0 && i%5 == 0 {
			L.Warn(email, fmt.Sprintf("等待 iframe 表单... (已等 %ds)", (i+1)*2))
		}
	}
	return fmt.Errorf("age_verify: 支付 iframe 60秒内未加载完成")
}

// fillCardForm 填写卡号、有效期、安全码、邮编（美国表单）。
func fillCardForm(email string, fl playwright.FrameLocator, cfg config.Config) error {
	L.Info(email, "填写信用卡信息...")

	// 卡号
	if err := fl.Locator(`input[inputmode="numeric"]`).First().Fill(cfg.CardNumber); err != nil {
		return fmt.Errorf("age_verify: 填写卡号失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 有效期
	if err := fl.Locator(`input[aria-label*="Expiration"]`).First().Fill(cfg.CardExpiry); err != nil {
		return fmt.Errorf("age_verify: 填写有效期失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 安全码
	if err := fl.Locator(`input[inputmode="numeric"]`).Nth(2).Fill(cfg.CardCVV); err != nil {
		return fmt.Errorf("age_verify: 填写安全码失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 邮编
	zip := cfg.CardZip
	if zip == "" {
		zip = "97201"
	}
	if err := fl.Locator(`input[autocomplete="postal-code"]`).First().Fill(zip); err != nil {
		return fmt.Errorf("age_verify: 填写邮编失败: %w", err)
	}
	time.Sleep(2 * time.Second)

	return nil
}

// clickSubmit 点击 "Save and submit" 按钮。
func clickSubmit(email string, fl playwright.FrameLocator) error {
	L.Info(email, "点击 Save and submit...")
	// 用 class 匹配主操作按钮（蓝色填充按钮）
	btn := fl.Locator(`button.VfPpkd-LgbsSe-OWXEXe-k8QpJ`).Filter(playwright.LocatorFilterOptions{
		HasText: "Save and submit",
	})
	if err := btn.DispatchEvent("click", nil, playwright.LocatorDispatchEventOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("age_verify: 点击提交按钮失败: %w", err)
	}
	return nil
}

// waitForResult 等待年龄验证结果。
func waitForResult(email string, agePage playwright.Page, fl playwright.FrameLocator) error {
	L.Info(email, "等待验证结果...")
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)

		if strings.Contains(agePage.URL(), "age-verification/result") {
			L.OK(email, "年龄验证成功")
			return nil
		}

		bodyText, _ := agePage.Locator("body").TextContent()
		if strings.Contains(bodyText, "Your age is verified") {
			L.OK(email, "年龄验证成功")
			return nil
		}

		alertDiv := fl.Locator(`div[role="alert"]`).First()
		if cnt, _ := alertDiv.Count(); cnt > 0 {
			txt, _ := alertDiv.TextContent()
			if txt != "" {
				L.Fail(email, fmt.Sprintf("年龄验证失败: %s", txt))
				return errAgeVerification
			}
		}
	}
	return fmt.Errorf("age_verify: 60秒超时未获得验证结果, 当前页面: %s", agePage.URL())
}
