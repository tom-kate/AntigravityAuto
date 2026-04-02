package automation

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"antiauto/config"
	L "antiauto/logger"

	"github.com/playwright-community/playwright-go"
)

// iframeSelector 是支付 iframe 的选择器
const iframeSelector = `iframe[title="Complete your purchase"]`

// doAgeVerify 通过信用卡完成 Google 年龄验证。
// 直接打开 credit-card 页面，不选国家，直接填表提交。
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

	// 填写信用卡表单（不含邮编）
	if err := fillCardForm(email, fl, cfg); err != nil {
		return err
	}

	// 依次尝试 4/5/6 位随机邮编提交（邮编字段可能不存在，忽略错误）
	zipLengths := []int{4, 5, 6}
	zipInput := fl.Locator(`input[autocomplete="postal-code"]`).First()
	for _, digits := range zipLengths {
		zip := randomZip(digits)
		L.Info(email, fmt.Sprintf("尝试 %d 位邮编: %s", digits, zip))
		_ = zipInput.Fill(zip, playwright.LocatorFillOptions{Timeout: playwright.Float(2000)})
		time.Sleep(500 * time.Millisecond)

		if err := clickSubmit(email, fl); err != nil {
			return err
		}

		// 等待 15 秒看是否成功
		ok, err := checkResult(email, agePage, fl, 15)
		if ok {
			return nil
		}
		if err != nil {
			return err
		}
		L.Warn(email, fmt.Sprintf("%d 位邮编未通过, 换下一个", digits))
	}

	return fmt.Errorf("age_verify: 所有邮编尝试均未成功, 当前页面: %s", agePage.URL())
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

// fillCardForm 填写卡号、有效期、安全码、地址、城市（不含邮编）。
func fillCardForm(email string, fl playwright.FrameLocator, cfg config.Config) error {
	L.Info(email, "填写信用卡信息...")

	// 卡号 — 第一个 numeric input
	if err := fl.Locator(`input[inputmode="numeric"]`).First().Fill(cfg.CardNumber); err != nil {
		return fmt.Errorf("age_verify: 填写卡号失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 有效期 — aria-label 包含 "Expiration"
	if err := fl.Locator(`input[aria-label*="Expiration"]`).First().Fill(cfg.CardExpiry); err != nil {
		return fmt.Errorf("age_verify: 填写有效期失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 安全码 — 第三个 numeric input
	if err := fl.Locator(`input[inputmode="numeric"]`).Nth(2).Fill(cfg.CardCVV); err != nil {
		return fmt.Errorf("age_verify: 填写安全码失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// 详细地址（可能不存在，忽略错误）
	_ = fl.Locator(`input[type="search"]`).First().Fill("123 Main St", playwright.LocatorFillOptions{Timeout: playwright.Float(2000)})
	time.Sleep(500 * time.Millisecond)

	// 城市（可能不存在，忽略错误）
	_ = fl.Locator(`input[type="text"]`).Last().Fill("Portland", playwright.LocatorFillOptions{Timeout: playwright.Float(2000)})
	time.Sleep(500 * time.Millisecond)

	return nil
}

// clickSubmit 点击 "Save and submit" 按钮。
func clickSubmit(email string, fl playwright.FrameLocator) error {
	L.Info(email, "点击 Save and submit...")
	btn := fl.Locator(`button[jsname="LgbsSe"]`).Filter(playwright.LocatorFilterOptions{
		HasText: "Save and submit",
	})
	if err := btn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("age_verify: 点击提交按钮失败: %w", err)
	}
	return nil
}

// randomZip 生成指定位数的随机数字邮编。
func randomZip(digits int) string {
	s := ""
	for i := 0; i < digits; i++ {
		if i == 0 {
			s += fmt.Sprintf("%d", rand.Intn(9)+1) // 首位不为0
		} else {
			s += fmt.Sprintf("%d", rand.Intn(10))
		}
	}
	return s
}

// checkResult 在 waitSec 秒内检查是否验证成功。
// 返回 (true, nil) 表示成功，(false, nil) 表示超时未成功，(false, err) 表示明确失败。
func checkResult(email string, agePage playwright.Page, fl playwright.FrameLocator, waitSec int) (bool, error) {
	rounds := waitSec / 2
	if rounds < 1 {
		rounds = 1
	}
	for i := 0; i < rounds; i++ {
		time.Sleep(2 * time.Second)

		if strings.Contains(agePage.URL(), "age-verification/result") {
			L.OK(email, "年龄验证成功")
			return true, nil
		}

		bodyText, _ := agePage.Locator("body").TextContent()
		if strings.Contains(bodyText, "Your age is verified") {
			L.OK(email, "年龄验证成功")
			return true, nil
		}

		alertDiv := fl.Locator(`div[role="alert"]`).First()
		if cnt, _ := alertDiv.Count(); cnt > 0 {
			txt, _ := alertDiv.TextContent()
			if txt != "" && strings.Contains(txt, "card") {
				L.Fail(email, fmt.Sprintf("年龄验证失败: %s", txt))
				return false, errAgeVerification
			}
		}
	}
	return false, nil
}
