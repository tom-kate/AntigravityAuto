package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"antiauto/config"
)

// smsClient is lazily initialized with proxy support.
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

type PhoneResponse struct {
	Data struct {
		Mobile  string `json:"mobile"`
		PhoneID string `json:"phoneId"`
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

func GetPhoneNumber() (formatted string, raw string, phoneID string, err error) {
	url := "https://web.tqsms.xyz/api/phone/getPhone"

	payload := map[string]interface{}{
		"projectName": "Google / Gmail (\u8c37\u6b4c)----\u91d1\u5e01\uff1a1.59----\u53ef\u7528\uff1a[hkg]+852/\u9999\u6e2f/hongkong \u5728\u7ebf: 429 \u53ef\u7528: 426----channelId:1613871117276024844",
		"operator":    "0",
		"phone_num":   "",
		"scope":       "",
		"scope_black": "",
		"code":        config.App.SMSCode,
		"projectId":   nil,
		"address":     "",
		"channelId":   config.App.SMSChannelID,
	}

	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", "", "", err
	}

	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "https://web.tqsms.xyz")
	req.Header.Set("referer", "https://web.tqsms.xyz/")
	req.Header.Set("x-token", config.App.SMSToken)

	resp, err := getSMSClient().Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var phoneResp PhoneResponse
	if err := json.Unmarshal(body, &phoneResp); err != nil {
		return "", "", "", err
	}

	if !phoneResp.Success || phoneResp.Status != 200 {
		return "", "", "", fmt.Errorf("failed to get phone number: %s", phoneResp.Msg)
	}

	// Parse "(852)98477634" to "+852 98477634"
	mobileForInput := phoneResp.Data.Mobile
	if strings.HasPrefix(mobileForInput, "(") {
		parts := strings.SplitN(mobileForInput[1:], ")", 2)
		if len(parts) == 2 {
			mobileForInput = fmt.Sprintf("+%s %s", parts[0], parts[1])
		}
	}

	return mobileForInput, phoneResp.Data.Mobile, phoneResp.Data.PhoneID, nil
}

func GetSMSCode(rawPhoneNum, phoneID string) (string, error) {
	url := "https://web.tqsms.xyz/api/code/getCode"

	payload := map[string]interface{}{
		"code":      config.App.SMSCode,
		"projectId": nil,
		"phoneNum":  rawPhoneNum,
		"channelId": config.App.SMSChannelID,
		"phoneId":   phoneID,
	}

	payloadBytes, _ := json.Marshal(payload)

	for i := 0; i < 60; i++ {
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return "", err
		}

		req.Header.Set("accept", "application/json, text/plain, */*")
		req.Header.Set("content-type", "application/json")
		req.Header.Set("origin", "https://web.tqsms.xyz")
		req.Header.Set("referer", "https://web.tqsms.xyz/")
		req.Header.Set("x-token", config.App.SMSToken)

		resp, err := getSMSClient().Do(req)
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

		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("failed to get SMS code after 60 attempts")
}
