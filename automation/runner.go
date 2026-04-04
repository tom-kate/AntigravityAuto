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

	"github.com/playwright-community/playwright-go"
)

// ─── Hardcoded flow: Login → OAuth → CPA check → (phone bind → delete CPA → OAuth redo) → done ───

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
	oauthCallbackCh := make(chan error, 1)
	bctx.OnRequest(func(req playwright.Request) {
		reqURL := req.URL()
		if strings.HasPrefix(reqURL, "http://localhost:51121/oauth-callback") {
			parsed, parseErr := url.Parse(reqURL)
			if parseErr != nil {
				L.Fail(email, fmt.Sprintf("OAuth Callback URL 解析失败: %v", parseErr))
				select { case oauthCallbackCh <- parseErr: default: }
				return
			}
			code := parsed.Query().Get("code")
			if code == "" {
				errMsg := parsed.Query().Get("error")
				L.Fail(email, fmt.Sprintf("OAuth Callback 无 code: error=%s", errMsg))
				select { case oauthCallbackCh <- fmt.Errorf("no code: %s", errMsg): default: }
				return
			}
			// Complete OAuth flow locally
			if _, cbErr := api.CompleteOAuthFlow(code); cbErr != nil {
				L.Fail(email, fmt.Sprintf("OAuth 本地流程失败: %v", cbErr))
				// Check if it's an upload failure
				if strings.Contains(cbErr.Error(), "upload_cpa_failed") {
					select { case oauthCallbackCh <- errUploadFailed: default: }
				} else {
					select { case oauthCallbackCh <- cbErr: default: }
				}
			} else {
				L.OK(email, "OAuth 本地流程完成, 凭证已上传")
				select { case oauthCallbackCh <- nil: default: }
			}
		}
	})

	updateStatus(batchID, idx, db.StatusRunning, "login", "")

	// ─── Step 1: Login to Gmail ───
	L.Step(email, "开始登录...")
	if err := doLogin(email, account, page); err != nil {
		return err
	}

	// ─── Step 2: Accept family group invitation ───
	updateStatus(batchID, idx, db.StatusRunning, "family_accept", "")
	L.Step(email, "确认家庭组邀请...")
	if err := doFamilyAccept(email, bctx, page); err != nil {
		return err
	}

	// ─── Step 3: OAuth → CPA check → (phone bind / retry) ───
	const maxCPAPolls = 10    // polls (10 × 3s = 30s max wait)
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

	// ─── Quota check: detect dead accounts ───
	updateStatus(batchID, idx, db.StatusRunning, "check_quota", "")
	L.Step(email, "检查额度...")

	// Wait for CPA to sync the auth file and get authIndex
	var authIndex string
	for poll := 0; poll < 10; poll++ {
		if poll > 0 {
			time.Sleep(3 * time.Second)
		}
		files, err := api.GetAuthFiles()
		if err != nil {
			continue
		}
		for _, f := range files {
			if strings.EqualFold(f.Account, email) || strings.EqualFold(f.Email, email) {
				authIndex = f.AuthIndex
				break
			}
		}
		if authIndex != "" {
			break
		}
	}

	if authIndex != "" {
		quotas, err := api.GetQuota(authIndex)
		if err != nil {
			L.Warn(email, fmt.Sprintf("额度查询失败: %v (继续流程)", err))
		} else {
			needAgeVerify := false
			for _, q := range quotas {
				if q.ResetTime == "" {
					continue
				}
				resetT, parseErr := time.Parse(time.RFC3339, q.ResetTime)
				if parseErr != nil {
					continue
				}
				if time.Until(resetT) > 5*time.Hour {
					L.Warn(email, fmt.Sprintf("模型 %s 额度刷新时间 %s, 距现在 %.1f 小时, 需要年龄验证",
						q.Name, q.ResetTime, time.Until(resetT).Hours()))
					needAgeVerify = true
					break
				}
			}
			if needAgeVerify {
				// Attempt age verification with credit card (no retry)
				updateStatus(batchID, idx, db.StatusRunning, "age_verify", "")
				if verifyErr := doAgeVerify(email, page); verifyErr != nil {
					return errAgeVerification
				}
				// Age verified — re-do OAuth to get fresh credentials
				L.OK(email, "年龄验证通过, 重新 OAuth 授权...")
				if err := doOAuthWithRetry("年龄验证后 OAuth"); err != nil {
					return err
				}
			} else {
				L.OK(email, "额度检查通过")
			}
		}
	} else {
		L.Warn(email, "未能获取 authIndex, 跳过额度检查")
	}

	// If phone already bound, we're done — no need for CPA check
	if phoneBound {
		L.OK(email, "手机已绑定, 无需 CPA 检测, 完成")
		return nil
	}

	// ─── Single CPA poll round ───
	updateStatus(batchID, idx, db.StatusRunning, "check_cpa", "")
	L.Step(email, "检查 CPA 状态...")

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
	}

	// No validation URL after all polls — mark for manual review
	L.Warn(email, "CPA 轮询未获取到手机绑定链接, 标记为人工确认")
	return errManualCheck
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
			updateStatusWithOp(batchID, idx, db.StatusSuccess, "done", "", "成功")
			L.OK(email, "全部完成")
			return
		}

		// Recaptcha: mark as error and skip, no retry
		if err == errRecaptcha {
			updateStatusWithOp(batchID, idx, db.StatusError, "recaptcha", "出现人机验证", "人机验证")
			return
		}

		// Manual check: mark and skip, no retry
		if err == errManualCheck {
			updateStatusWithOp(batchID, idx, db.StatusError, "manual_check", "CPA 未返回手机绑定链接, 等待人工绑定", "等待人工绑定")
			return
		}

		// Upload failed: mark and skip, no retry
		if err == errUploadFailed {
			updateStatusWithOp(batchID, idx, db.StatusError, "upload_failed", "凭证上传 CPA 失败 (已重试3次)", "凭证上传失败")
			return
		}

		// Need restart: mark and skip, no retry
		if err == errNeedRestart {
			updateStatusWithOp(batchID, idx, db.StatusError, "need_restart", "无需手机绑定, 需要重新授权", "需要重新授权")
			return
		}

		// Quota dead: mark and skip, no retry
		if err == errQuotaDead {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "quota_dead", "额度刷新时间超过5小时, 账号判定死亡", "账号死亡")
			return
		}

		// Age verification failed (card invalid or unknown page): mark as failed, no retry
		if err == errAgeVerification {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "age_verify", "年龄异常: 信用卡验证失败", "年龄异常")
			return
		}

		// Family country mismatch: mark as failed, no retry
		if err == errFamilyCountry {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "family_country", "国家不支持, 无法加入家庭组", "国家不支持")
			return
		}

		// Already in another family group: mark as failed, no retry
		if err == errFamilyAlreadyInGroup {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "family_already_in_group", "已在其他家庭组中, 无法加入", "已在家庭组")
			return
		}

		// Family invitation email not found: mark as failed, no retry
		if err == errFamilyNotFound {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "family_not_found", "未找到家庭组邀请邮件", "未找到家庭组")
			return
		}

		// GCP banned: mark as failed, no retry
		if err == errGCPBanned {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "gcp_banned", "GCP 已被封禁, 账号不可用", "封GCP")
			return
		}

		errMsg := fmt.Sprintf("第 %d/%d 次: %v", attempt, db.MaxRetries, err)
		L.Fail(email, errMsg)

		if attempt < db.MaxRetries {
			updateStatus(batchID, idx, db.StatusRunning, "retry_wait", errMsg)
			time.Sleep(10 * time.Second)
		} else {
			updateStatusWithOp(batchID, idx, db.StatusFailed, "exhausted", errMsg, "重试耗尽")
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

	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
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

// RunPhoneBind runs a standalone phone binding for a single sub account.
// It logs in, gets validation URL from CPA, and performs phone binding.
func RunPhoneBind(batchID string, idx int) {
	batch := db.DB.GetBatch(batchID)
	if batch == nil || idx >= len(batch.Accounts) {
		return
	}

	account := batch.Accounts[idx]
	email := account.Email

	// Prevent duplicate runs
	if account.Status == db.StatusRunning {
		L.Warn(email, "该账号正在执行中, 请勿重复操作")
		return
	}

	L.Banner(fmt.Sprintf("手动手机绑定: %s", email))
	updateStatusWithOp(batchID, idx, db.StatusRunning, "phone_bind", "", "绑定手机中")

	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
		L.Fail(email, fmt.Sprintf("Playwright 安装失败: %v", err))
		updateStatus(batchID, idx, db.StatusError, "phone_bind", fmt.Sprintf("Playwright 安装失败: %v", err))
		return
	}

	pw, err := playwright.Run()
	if err != nil {
		L.Fail(email, fmt.Sprintf("Playwright 启动失败: %v", err))
		updateStatus(batchID, idx, db.StatusError, "phone_bind", fmt.Sprintf("Playwright 启动失败: %v", err))
		return
	}
	defer pw.Stop()

	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--disable-infobars",
			"--disable-extensions",
		},
	}
	if config.Get().Headless {
		launchOpts.Args = append(launchOpts.Args, "--headless=new")
	}
	if cfg := config.Get(); cfg.ProxyEnabled && cfg.Proxy != "" {
		launchOpts.Proxy = &playwright.Proxy{Server: cfg.Proxy}
	}

	browser, err := pw.Chromium.Launch(launchOpts)
	if err != nil {
		L.Fail(email, fmt.Sprintf("浏览器启动失败: %v", err))
		updateStatus(batchID, idx, db.StatusError, "phone_bind", fmt.Sprintf("浏览器启动失败: %v", err))
		return
	}
	defer browser.Close()

	bctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
	})
	if err != nil {
		updateStatus(batchID, idx, db.StatusError, "phone_bind", "创建浏览器上下文失败")
		return
	}
	defer bctx.Close()

	page, err := bctx.NewPage()
	if err != nil {
		updateStatus(batchID, idx, db.StatusError, "phone_bind", "创建页面失败")
		return
	}

	// Step 1: Login
	L.Step(email, "登录中...")
	if err := doLogin(email, account, page); err != nil {
		L.Fail(email, fmt.Sprintf("登录失败: %v", err))
		updateStatus(batchID, idx, db.StatusError, "phone_bind", fmt.Sprintf("登录失败: %v", err))
		return
	}

	// Step 2: Get validation URL from CPA
	L.Step(email, "获取手机绑定链接...")
	var validationURL string
	for poll := 0; poll < 10; poll++ {
		if poll > 0 {
			time.Sleep(3 * time.Second)
		}
		status, vURL, err := api.CheckCPAOnce(email)
		if err != nil {
			continue
		}
		if status == api.CPAHasURL {
			validationURL = vURL
			break
		}
	}

	if validationURL == "" {
		L.Fail(email, "未获取到手机绑定链接")
		updateStatus(batchID, idx, db.StatusError, "phone_bind", "CPA 未返回手机绑定链接")
		return
	}

	L.Info(email, fmt.Sprintf("手机绑定链接: %s", validationURL))

	// Step 3: Phone binding
	err = doPhoneBind(email, page, validationURL, batchID, idx)
	if err == errNeedRestart {
		updateStatus(batchID, idx, db.StatusError, "need_restart", "无需手机绑定, 需要重新授权")
		return
	}
	if err != nil {
		L.Fail(email, fmt.Sprintf("手机绑定失败: %v", err))
		updateStatus(batchID, idx, db.StatusError, "phone_bind", fmt.Sprintf("手机绑定失败: %v", err))
		return
	}

	// Step 4: Delete CPA file and re-OAuth
	L.Info(email, "手机绑定完成, 删除 CPA 凭证...")
	api.DeleteAuthFile(email)
	time.Sleep(3 * time.Second)

	// Re-OAuth with callback handling
	oauthCallbackCh := make(chan error, 1)
	bctx.OnRequest(func(req playwright.Request) {
		reqURL := req.URL()
		if strings.HasPrefix(reqURL, "http://localhost:51121/oauth-callback") {
			parsed, _ := url.Parse(reqURL)
			code := parsed.Query().Get("code")
			if code == "" {
				select { case oauthCallbackCh <- fmt.Errorf("no code"): default: }
				return
			}
			if _, cbErr := api.CompleteOAuthFlow(code); cbErr != nil {
				select { case oauthCallbackCh <- cbErr: default: }
			} else {
				select { case oauthCallbackCh <- nil: default: }
			}
		}
	})

	L.Step(email, "重新 OAuth 授权...")
	if err := doOAuthStart(email, account, bctx, oauthCallbackCh); err != nil {
		L.Fail(email, fmt.Sprintf("重新 OAuth 失败: %v", err))
		updateStatus(batchID, idx, db.StatusError, "oauth", fmt.Sprintf("重新 OAuth 失败: %v", err))
		return
	}

	updateStatusWithOp(batchID, idx, db.StatusSuccess, "done", "", "成功")
	L.OK(email, "手动手机绑定 + 重新授权完成")
}

// RunAgeVerify runs a standalone age verification for a single sub account.
// It logs in, performs credit card age verification, then re-does OAuth.
func RunAgeVerify(batchID string, idx int) {
	batch := db.DB.GetBatch(batchID)
	if batch == nil || idx >= len(batch.Accounts) {
		return
	}

	account := batch.Accounts[idx]
	email := account.Email

	// Prevent duplicate runs
	if account.Status == db.StatusRunning {
		L.Warn(email, "该账号正在执行中, 请勿重复操作")
		return
	}

	L.Banner(fmt.Sprintf("手动年龄验证: %s", email))
	updateStatusWithOp(batchID, idx, db.StatusRunning, "age_verify", "", "年龄验证中")

	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
		L.Fail(email, fmt.Sprintf("Playwright 安装失败: %v", err))
		updateStatusWithOp(batchID, idx, db.StatusError, "age_verify", fmt.Sprintf("Playwright 安装失败: %v", err), "年龄异常")
		return
	}

	pw, err := playwright.Run()
	if err != nil {
		L.Fail(email, fmt.Sprintf("Playwright 启动失败: %v", err))
		updateStatusWithOp(batchID, idx, db.StatusError, "age_verify", fmt.Sprintf("Playwright 启动失败: %v", err), "年龄异常")
		return
	}
	defer pw.Stop()

	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--disable-infobars",
			"--disable-extensions",
		},
	}
	if config.Get().Headless {
		launchOpts.Args = append(launchOpts.Args, "--headless=new")
	}
	if cfg := config.Get(); cfg.ProxyEnabled && cfg.Proxy != "" {
		launchOpts.Proxy = &playwright.Proxy{Server: cfg.Proxy}
	}

	browser, err := pw.Chromium.Launch(launchOpts)
	if err != nil {
		L.Fail(email, fmt.Sprintf("浏览器启动失败: %v", err))
		updateStatusWithOp(batchID, idx, db.StatusError, "age_verify", fmt.Sprintf("浏览器启动失败: %v", err), "年龄异常")
		return
	}
	defer browser.Close()

	bctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
	})
	if err != nil {
		updateStatusWithOp(batchID, idx, db.StatusError, "age_verify", "创建浏览器上下文失败", "年龄异常")
		return
	}
	defer bctx.Close()

	page, err := bctx.NewPage()
	if err != nil {
		updateStatusWithOp(batchID, idx, db.StatusError, "age_verify", "创建页面失败", "年龄异常")
		return
	}

	// Step 1: Login
	L.Step(email, "登录中...")
	if err := doLogin(email, account, page); err != nil {
		L.Fail(email, fmt.Sprintf("登录失败: %v", err))
		updateStatusWithOp(batchID, idx, db.StatusError, "age_verify", fmt.Sprintf("登录失败: %v", err), "年龄异常")
		return
	}

	// Step 2: Age verification
	L.Step(email, "开始年龄验证...")
	if err := doAgeVerify(email, page); err != nil {
		L.Fail(email, fmt.Sprintf("年龄验证失败: %v", err))
		updateStatusWithOp(batchID, idx, db.StatusFailed, "age_verify", fmt.Sprintf("年龄验证失败: %v", err), "年龄异常")
		return
	}

	// Step 3: Delete CPA file and re-OAuth
	L.Info(email, "年龄验证通过, 删除 CPA 凭证并重新授权...")
	api.DeleteAuthFile(email)
	time.Sleep(3 * time.Second)

	// Re-OAuth with callback handling
	oauthCallbackCh := make(chan error, 1)
	bctx.OnRequest(func(req playwright.Request) {
		reqURL := req.URL()
		if strings.HasPrefix(reqURL, "http://localhost:51121/oauth-callback") {
			parsed, _ := url.Parse(reqURL)
			code := parsed.Query().Get("code")
			if code == "" {
				select { case oauthCallbackCh <- fmt.Errorf("no code"): default: }
				return
			}
			if _, cbErr := api.CompleteOAuthFlow(code); cbErr != nil {
				select { case oauthCallbackCh <- cbErr: default: }
			} else {
				select { case oauthCallbackCh <- nil: default: }
			}
		}
	})

	L.Step(email, "重新 OAuth 授权...")
	if err := doOAuthStart(email, account, bctx, oauthCallbackCh); err != nil {
		L.Fail(email, fmt.Sprintf("重新 OAuth 失败: %v", err))
		updateStatusWithOp(batchID, idx, db.StatusError, "oauth", fmt.Sprintf("重新 OAuth 失败: %v", err), "年龄异常")
		return
	}

	updateStatusWithOp(batchID, idx, db.StatusSuccess, "done", "", "成功")
	L.OK(email, "手动年龄验证 + 重新授权完成")
}
