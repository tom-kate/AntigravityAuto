package automation

import (
	"fmt"
	"strings"

	"antiauto/db"
)

// Navigation timeout for slow networks (120 seconds)
const navTimeout = 120000

// errRecaptcha is a sentinel error indicating a recaptcha challenge was encountered.
var errRecaptcha = fmt.Errorf("recaptcha: 出现人机验证")

// errManualCheck is a sentinel error indicating the account needs manual review.
var errManualCheck = fmt.Errorf("manual_check: 需要人工确认")

// errUploadFailed is a sentinel error indicating credential upload to CPA failed.
var errUploadFailed = fmt.Errorf("upload_failed: 凭证上传失败")

// errNeedRestart is a sentinel error indicating account needs re-OAuth (phone binding not actually needed).
var errNeedRestart = fmt.Errorf("need_restart: 需要重新授权")

// errQuotaDead is a sentinel error indicating the account quota reset time exceeds 5 hours.
var errQuotaDead = fmt.Errorf("quota_dead: 额度刷新时间超过5小时, 账号判定死亡")

// errFamilyCountry is a sentinel error indicating the account's country doesn't match the family group.
var errFamilyCountry = fmt.Errorf("family_country: 国家不支持, 无法加入家庭组")

// checkRecaptcha checks if the current URL is a recaptcha challenge page.
func checkRecaptcha(pageURL string) bool {
	return strings.Contains(pageURL, "signin/challenge/recaptcha")
}

func updateStatus(batchID string, idx int, status db.SubAccountStatus, step string, errMsg string) {
	db.DB.UpdateSubAccount(batchID, idx, status, step, errMsg)
}
