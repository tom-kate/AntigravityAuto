package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"antiauto/config"
)

// ─── SMS Platform Client ────────────────────────────────────────

var (
	smsClient     *http.Client
	smsClientOnce sync.Once
	smsToken      string // runtime token, obtained via login
	smsTokenMu    sync.RWMutex
)

func getSMSClient() *http.Client {
	smsClientOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		cfg := config.Get()
		if cfg.ProxyEnabled && cfg.Proxy != "" {
			if proxyURL, err := url.Parse(cfg.Proxy); err == nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
		}
		smsClient = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	})
	return smsClient
}

func getSMSToken() string {
	smsTokenMu.RLock()
	defer smsTokenMu.RUnlock()
	return smsToken
}

// ─── Login ──────────────────────────────────────────────────────

type smsLoginResponse struct {
	Data struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	} `json:"data"`
	Msg     string `json:"msg"`
	Status  int    `json:"status"`
	Success bool   `json:"success"`
}

// SMSLogin logs into the SMS platform and stores the token.
// Should be called once at startup.
func SMSLogin() error {
	cfg := config.Get()
	if cfg.SMSUsername == "" || cfg.SMSPassword == "" {
		log.Println("短信平台: 未配置账号密码, 跳过登录")
		return nil
	}

	apiURL := fmt.Sprintf("https://web.tqsms.xyz/api/login?username=%s&password=%s",
		url.QueryEscape(cfg.SMSUsername), url.QueryEscape(cfg.SMSPassword))

	resp, err := getSMSClient().Get(apiURL)
	if err != nil {
		return fmt.Errorf("短信平台登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var loginResp smsLoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return fmt.Errorf("短信平台登录响应解析失败: %w", err)
	}

	if !loginResp.Success || loginResp.Status != 200 || loginResp.Data.Token == "" {
		return fmt.Errorf("短信平台登录失败: %s", loginResp.Msg)
	}

	smsTokenMu.Lock()
	smsToken = loginResp.Data.Token
	smsTokenMu.Unlock()

	log.Printf("短信平台登录成功: %s (token: %s...)", loginResp.Data.Name, loginResp.Data.Token[:20])
	return nil
}

// ─── Response Types ─────────────────────────────────────────────

type PhoneResponse struct {
	Data struct {
		Mobile  string `json:"mobile"`
		SmsTask struct {
			ID      int    `json:"id"`
			PhoneNo string `json:"phoneNo"`
		} `json:"smsTask"`
	} `json:"data"`
	Msg     string `json:"msg"`
	Status  int    `json:"status"`
	Success bool   `json:"success"`
}

type CodeResponse struct {
	Data struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"data"`
	Msg     string `json:"msg"`
	Status  int    `json:"status"`
	Success bool   `json:"success"`
}

// ─── Get Phone Number ───────────────────────────────────────────

func GetPhoneNumber() (formatted string, raw string, phoneID string, err error) {
	token := getSMSToken()
	if token == "" {
		return "", "", "", fmt.Errorf("短信平台未登录, 无 token")
	}

	cfg := config.Get()
	apiURL := fmt.Sprintf("https://web.tqsms.xyz/api/getPhone?token=%s&channelId=%s&operator=0",
		url.QueryEscape(token), url.QueryEscape(cfg.SMSChannelID))

	resp, err := getSMSClient().Get(apiURL)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var phoneResp PhoneResponse
	if err := json.Unmarshal(body, &phoneResp); err != nil {
		return "", "", "", fmt.Errorf("解析手机号响应失败: %w (body: %s)", err, string(body))
	}

	if !phoneResp.Success || phoneResp.Status != 200 {
		return "", "", "", fmt.Errorf("获取手机号失败: %s", phoneResp.Msg)
	}

	mobile := phoneResp.Data.Mobile
	if mobile == "" {
		mobile = phoneResp.Data.SmsTask.PhoneNo
	}
	if mobile == "" {
		return "", "", "", fmt.Errorf("获取手机号失败: 返回为空")
	}

	// Format phone for Google input
	// "(852)98477634" → "+852 98477634"
	// "15664864435" → "+86 15664864435" (if no country code prefix)
	mobileForInput := mobile
	if strings.HasPrefix(mobileForInput, "(") {
		parts := strings.SplitN(mobileForInput[1:], ")", 2)
		if len(parts) == 2 {
			mobileForInput = fmt.Sprintf("+%s %s", parts[0], parts[1])
		}
	}

	// Use smsTask.ID as phoneID (for getCode compatibility)
	pid := fmt.Sprintf("%d", phoneResp.Data.SmsTask.ID)

	return mobileForInput, mobile, pid, nil
}

// ─── Get SMS Code ───────────────────────────────────────────────

func GetSMSCode(rawPhoneNum, phoneID string) (string, error) {
	token := getSMSToken()
	if token == "" {
		return "", fmt.Errorf("短信平台未登录, 无 token")
	}

	cfg := config.Get()

	for i := 0; i < 60; i++ {
		apiURL := fmt.Sprintf("https://web.tqsms.xyz/api/getCode?token=%s&channelId=%s&phoneNum=%s",
			url.QueryEscape(token), url.QueryEscape(cfg.SMSChannelID), url.QueryEscape(rawPhoneNum))

		resp, err := getSMSClient().Get(apiURL)
		if err != nil {
			return "", err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var codeResp CodeResponse
		if err := json.Unmarshal(body, &codeResp); err != nil {
			return "", err
		}

		if codeResp.Success && codeResp.Status == 200 && codeResp.Data.Code != "" {
			return codeResp.Data.Code, nil
		}

		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("获取短信验证码失败: 60次轮询后未收到")
}

// ─── Release Phone ──────────────────────────────────────────────

// ReleasePhone releases a phone number back to the pool.
func ReleasePhone(phoneNo string) error {
	token := getSMSToken()
	if token == "" {
		return nil
	}
	cfg := config.Get()
	apiURL := fmt.Sprintf("https://web.tqsms.xyz/api/release?token=%s&channelId=%s&phoneNo=%s&status=2",
		url.QueryEscape(token), url.QueryEscape(cfg.SMSChannelID), url.QueryEscape(phoneNo))

	resp, err := getSMSClient().Get(apiURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
