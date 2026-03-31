package automation

import (
	"fmt"
	"strings"
	"time"

	L "antiauto/logger"

	"github.com/playwright-community/playwright-go"
)

// dismissSmartFeaturesDialog handles the Google Workspace "smart features" initialization dialog.
// It can appear on inbox or email detail pages. Two steps:
//   Step 1: select first option (Turn on) → click Next
//   Step 2: select first option (Turn on) → click Save
func dismissSmartFeaturesDialog(email string, page playwright.Page) {
	// Check if the dialog is present
	dialog := page.Locator(`div.fpclHd[jsname="jMFtq"]`)
	if cnt, _ := dialog.Count(); cnt == 0 {
		return
	}

	L.Info(email, "检测到 Google Workspace 智能功能弹窗, 处理中...")

	// Step 1: click first radio option
	firstOption := page.Locator(`li[jsname="fiTGvf"]`)
	if cnt, _ := firstOption.Count(); cnt > 0 {
		_ = firstOption.First().Click()
		time.Sleep(1 * time.Second)
	}

	// Click "Next" button
	nextBtn := page.Locator(`button[jsname="OCpkoe"]`)
	if cnt, _ := nextBtn.Count(); cnt > 0 {
		_ = nextBtn.Click()
		time.Sleep(2 * time.Second)
	}

	// Step 2: click first radio option on second page
	secondOption := page.Locator(`li[jsname="XTJo3"]`)
	if cnt, _ := secondOption.Count(); cnt > 0 {
		_ = secondOption.First().Click()
		time.Sleep(1 * time.Second)
	}

	// Click "Save" button
	saveBtn := page.Locator(`button[jsname="plIjzf"]`)
	if cnt, _ := saveBtn.Count(); cnt > 0 {
		_ = saveBtn.Click()
		time.Sleep(2 * time.Second)
	}

	L.OK(email, "智能功能弹窗已处理")
}

// doFamilyAccept finds and accepts a Google Family Group invitation from the Gmail inbox.
// The page should already be on the Gmail inbox (mail.google.com/mail/u/0).
func doFamilyAccept(email string, bctx playwright.BrowserContext, page playwright.Page) error {
	// Ensure we're on the Gmail inbox
	currentURL := page.URL()
	if !strings.Contains(currentURL, "mail.google.com") {
		L.Info(email, "导航到 Gmail 收件箱...")
		if _, err := page.Goto("https://mail.google.com/mail/u/0/#inbox", playwright.PageGotoOptions{
			Timeout: playwright.Float(navTimeout),
		}); err != nil {
			return fmt.Errorf("family: navigate to inbox failed: %w", err)
		}
		time.Sleep(3 * time.Second)
	}

	// Wait for inbox to load — look for the email table
	L.Info(email, "等待收件箱加载...")
	table := page.Locator("table.F.cf.zt")
	if err := table.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		return fmt.Errorf("family: inbox table not found: %w", err)
	}
	time.Sleep(2 * time.Second)

	// Dismiss smart features dialog if present (can appear on inbox page)
	dismissSmartFeaturesDialog(email, page)

	// Find the family group email: from families-noreply@google.com, subject contains "?"
	L.Info(email, "查找家庭组邀请邮件...")

	// Wait for email rows to render (table frame loads before rows)
	// Use tr[role="row"] to match both read and unread emails (tr.zA=unread, tr.yO=read)
	rows := page.Locator(`tr[role="row"]`)
	if err := rows.First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		return fmt.Errorf("family: 收件箱无邮件: %w", err)
	}
	count, _ := rows.Count()

	foundIdx := -1
	for i := 0; i < count; i++ {
		row := rows.Nth(i)
		// Check sender (zF=unread, yP=read)
		sender := row.Locator(`span[email="families-noreply@google.com"]`)
		if cnt, _ := sender.Count(); cnt > 0 {
			foundIdx = i
			break
		}
	}

	if foundIdx == -1 {
		return fmt.Errorf("family: 未找到家庭组邀请邮件")
	}

	L.Info(email, fmt.Sprintf("找到家庭组邮件 (第 %d 行), 点击打开...", foundIdx+1))
	if err := rows.Nth(foundIdx).Click(); err != nil {
		return fmt.Errorf("family: click email row failed: %w", err)
	}

	// Wait for email detail to load
	time.Sleep(3 * time.Second)

	// Dismiss smart features dialog if present (can also appear on email detail page)
	dismissSmartFeaturesDialog(email, page)

	// Find the invite CTA button link — the blue styled button (background:#1a73e8)
	// Gmail emails contain multiple notifications.googleapis.com links (tracking, unsubscribe, etc.)
	// The actual invite button is the one styled as a blue CTA button
	L.Info(email, "提取家庭组邀请链接...")

	var href string

	// Strategy 1: find the blue CTA button
	ctaLink := page.Locator(`a[href*="notifications.googleapis.com/email/redirect"][style*="1a73e8"]`)
	if cnt, _ := ctaLink.Count(); cnt > 0 {
		href, _ = ctaLink.First().GetAttribute("href")
	}

	// Strategy 2: fallback — pick the link with the longest href (real invite link has a long r= parameter)
	if href == "" {
		allLinks := page.Locator(`a[href*="notifications.googleapis.com/email/redirect"]`)
		linkCount, _ := allLinks.Count()
		for i := 0; i < linkCount; i++ {
			h, _ := allLinks.Nth(i).GetAttribute("href")
			if len(h) > len(href) {
				href = h
			}
		}
	}

	if href == "" {
		return fmt.Errorf("family: invite link not found in email")
	}

	L.Info(email, fmt.Sprintf("邀请链接长度: %d, 通过 window.open 打开...", len(href)))
	familyPage, err := page.ExpectPopup(func() error {
		_, evalErr := page.Evaluate(`url => window.open(url)`, href)
		return evalErr
	})
	if err != nil {
		return fmt.Errorf("family: open invite link failed: %w", err)
	}
	defer familyPage.Close()

	// Wait for the redirect chain to settle — use networkidle to ensure full page load
	if err := familyPage.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(navTimeout),
	}); err != nil {
		return fmt.Errorf("family: wait for family page load failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Should be on: https://myaccount.google.com/family/join/XXXX
	familyURL := familyPage.URL()
	if !strings.Contains(familyURL, "myaccount.google.com/family/join") {
		return fmt.Errorf("family: unexpected page after invite link: %s", familyURL)
	}

	// Click "Join family" submit button (type="submit" to distinguish from Skip/Help buttons)
	L.Info(email, "点击加入家庭组...")
	joinBtn := familyPage.Locator(`button[type="submit"][jsname="Pr7Yme"]`)
	if err := joinBtn.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		return fmt.Errorf("family: join button not found: %w", err)
	}
	if err := joinBtn.Click(); err != nil {
		return fmt.Errorf("family: click join button failed: %w", err)
	}

	// Wait for success or error page
	L.Info(email, "等待加入结果...")
	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Second)
		resultURL := familyPage.URL()
		if strings.Contains(resultURL, "family/join/success") {
			break
		}
		if strings.Contains(resultURL, "family/join/error") {
			L.Fail(email, "加入家庭组失败: 国家不支持")
			return errFamilyCountry
		}
		// Check for "already in another family group" error on page
		alreadyInGroup := familyPage.Locator(`h2.u4nkWe`)
		if cnt, _ := alreadyInGroup.Count(); cnt > 0 {
			txt, _ := alreadyInGroup.First().TextContent()
			if strings.Contains(txt, "one Family Group at a time") {
				L.Fail(email, "加入家庭组失败: 已在其他家庭组中 (You can only be part of one Family Group at a time)")
				return errFamilyAlreadyInGroup
			}
		}
		if i == 19 {
			return fmt.Errorf("family: 加入家庭组失败, 未跳转到成功页面, 当前: %s", familyPage.URL())
		}
	}

	L.OK(email, "加入家庭组成功")

	// Click "View family group" confirm button on success page
	confirmBtn := familyPage.Locator(`button[type="submit"][jsname="Pr7Yme"]`)
	if cnt, _ := confirmBtn.Count(); cnt > 0 {
		_ = confirmBtn.Click()
		time.Sleep(2 * time.Second)
	}

	// familyPage will be closed by defer; return to original page
	return nil
}
