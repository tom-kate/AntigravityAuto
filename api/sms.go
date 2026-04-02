package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"antiauto/config"
)

// ─── SMS Platform Client (HeroSMS / SMS-Activate compatible) ───

var (
	smsClient     *http.Client
	smsClientOnce sync.Once
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

// smsRequest makes a GET request to the SMS API with the given action and extra params.
func smsRequest(action string, extra map[string]string) (string, error) {
	cfg := config.Get()
	if cfg.SMSApiKey == "" {
		return "", fmt.Errorf("SMS API Key 未配置")
	}
	if cfg.SMSApiURL == "" {
		return "", fmt.Errorf("SMS API URL 未配置")
	}

	u := fmt.Sprintf("%s?action=%s&api_key=%s", cfg.SMSApiURL, url.QueryEscape(action), url.QueryEscape(cfg.SMSApiKey))
	for k, v := range extra {
		u += fmt.Sprintf("&%s=%s", url.QueryEscape(k), url.QueryEscape(v))
	}

	resp, err := getSMSClient().Get(u)
	if err != nil {
		return "", fmt.Errorf("SMS API 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(body)), nil
}

// ─── Get Balance ───────────────────────────────────────────────

// GetSMSBalance returns the current SMS platform balance.
func GetSMSBalance() (float64, error) {
	result, err := smsRequest("getBalance", nil)
	if err != nil {
		return 0, err
	}
	// ACCESS_BALANCE:100.5
	if !strings.HasPrefix(result, "ACCESS_BALANCE:") {
		return 0, fmt.Errorf("SMS 余额查询失败: %s", result)
	}
	balance, err := strconv.ParseFloat(strings.TrimPrefix(result, "ACCESS_BALANCE:"), 64)
	if err != nil {
		return 0, fmt.Errorf("SMS 余额解析失败: %s", result)
	}
	return balance, nil
}

// ─── Get Phone Number ──────────────────────────────────────────

// GetPhoneNumber requests a phone number for the configured service and country.
// Returns: formatted phone for input, activationID, error
func GetPhoneNumber() (formatted string, activationID string, err error) {
	cfg := config.Get()
	if cfg.SMSService == "" {
		return "", "", fmt.Errorf("SMS 服务代码未配置")
	}

	result, err := smsRequest("getNumber", map[string]string{
		"service": cfg.SMSService,
		"country": strconv.Itoa(cfg.SMSCountry),
	})
	if err != nil {
		return "", "", err
	}

	// ACCESS_NUMBER:activation_id:phone_number
	if !strings.HasPrefix(result, "ACCESS_NUMBER:") {
		return "", "", fmt.Errorf("获取手机号失败: %s", result)
	}

	parts := strings.SplitN(result, ":", 3)
	if len(parts) != 3 {
		return "", "", fmt.Errorf("获取手机号响应格式错误: %s", result)
	}

	activationID = parts[1]
	phone := parts[2]

	// Format: prepend + if not present
	formattedPhone := phone
	if !strings.HasPrefix(formattedPhone, "+") {
		formattedPhone = "+" + formattedPhone
	}

	return formattedPhone, activationID, nil
}

// ─── Get SMS Code ──────────────────────────────────────────────

// GetSMSCode polls for the SMS verification code for the given activation.
func GetSMSCode(activationID string) (string, error) {
	for i := 0; i < 20; i++ {
		result, err := smsRequest("getStatus", map[string]string{
			"id": activationID,
		})
		if err != nil {
			return "", err
		}

		// STATUS_OK:code — code received
		if strings.HasPrefix(result, "STATUS_OK:") {
			code := strings.TrimPrefix(result, "STATUS_OK:")
			return code, nil
		}

		// STATUS_CANCEL — activation was canceled
		if result == "STATUS_CANCEL" {
			return "", fmt.Errorf("激活已被取消")
		}

		// STATUS_WAIT_CODE / STATUS_WAIT_RESEND — keep polling
		time.Sleep(3 * time.Second)
	}

	return "", fmt.Errorf("获取短信验证码失败: 60秒轮询后未收到")
}

// ─── Set Status (complete / cancel) ────────────────────────────

// SetActivationStatus sets the activation status.
// status: 6 = complete, 8 = cancel
func SetActivationStatus(activationID string, status int) error {
	result, err := smsRequest("setStatus", map[string]string{
		"id":     activationID,
		"status": strconv.Itoa(status),
	})
	if err != nil {
		return err
	}

	// Accept known success responses
	switch result {
	case "ACCESS_ACTIVATION", "ACCESS_CANCEL", "ACCESS_RETRY_GET":
		return nil
	}
	return fmt.Errorf("设置激活状态失败: %s", result)
}

// ReleasePhone cancels an activation and returns the money.
func ReleasePhone(activationID string) error {
	return SetActivationStatus(activationID, 8)
}

// ─── Countries & Services (for settings UI) ────────────────────

type SMSCountry struct {
	ID      int    `json:"id"`
	Eng     string `json:"eng"`
	Chn     string `json:"chn"`
	Visible int    `json:"visible"`
}

type SMSService struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// GetSMSCountries returns available countries.
func GetSMSCountries() ([]SMSCountry, error) {
	result, err := smsRequest("getCountries", nil)
	if err != nil {
		return nil, err
	}

	var countries []SMSCountry
	if err := json.Unmarshal([]byte(result), &countries); err != nil {
		return nil, fmt.Errorf("解析国家列表失败: %w (body: %.200s)", err, result)
	}

	// Filter to visible only
	var visible []SMSCountry
	for _, c := range countries {
		if c.Visible == 1 {
			visible = append(visible, c)
		}
	}
	return visible, nil
}

type smsServicesResponse struct {
	Status   string       `json:"status"`
	Services []SMSService `json:"services"`
}

// GetSMSServices returns available services for the given country.
func GetSMSServices(country int) ([]SMSService, error) {
	result, err := smsRequest("getServicesList", map[string]string{
		"country": strconv.Itoa(country),
		"lang":    "cn",
	})
	if err != nil {
		return nil, err
	}

	var resp smsServicesResponse
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return nil, fmt.Errorf("解析服务列表失败: %w (body: %.200s)", err, result)
	}

	if resp.Status != "success" {
		return nil, fmt.Errorf("获取服务列表失败: %s", result)
	}

	return resp.Services, nil
}

// SMSPriceInfo holds price and availability for a service in a country.
type SMSPriceInfo struct {
	Cost  float64 `json:"cost"`
	Count int     `json:"count"`
}

// GetSMSPrices returns prices for a specific country and service.
func GetSMSPrices(country int, service string) (*SMSPriceInfo, error) {
	params := map[string]string{
		"country": strconv.Itoa(country),
	}
	if service != "" {
		params["service"] = service
	}

	result, err := smsRequest("getPrices", params)
	if err != nil {
		return nil, err
	}

	// Response: { "country_id": { "service_code": { "cost": 0.5, "count": 10 } } }
	var data map[string]map[string]SMSPriceInfo
	if err := json.Unmarshal([]byte(result), &data); err != nil {
		return nil, fmt.Errorf("解析价格失败: %w (body: %.200s)", err, result)
	}

	countryStr := strconv.Itoa(country)
	if countryData, ok := data[countryStr]; ok {
		if priceInfo, ok := countryData[service]; ok {
			return &priceInfo, nil
		}
	}

	return nil, fmt.Errorf("未找到该服务的价格信息")
}

// TestGetNumber does a test: get a number then immediately cancel it.
// Returns the phone number obtained.
func TestGetNumber() (string, string, error) {
	formatted, activationID, err := GetPhoneNumber()
	if err != nil {
		return "", "", err
	}
	return formatted, activationID, nil
}
