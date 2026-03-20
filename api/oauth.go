package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"antiauto/config"
)

// ─── Proxy-aware HTTP client for Google API calls ────────────────

var (
	oauthClient     *http.Client
	oauthClientOnce sync.Once
)

func getOAuthClient() *http.Client {
	oauthClientOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		cfg := config.Get()
		if cfg.ProxyEnabled && cfg.Proxy != "" {
			if proxyURL, err := url.Parse(cfg.Proxy); err == nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
		}
		oauthClient = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	})
	return oauthClient
}

// ─── Antigravity OAuth Constants ─────────────────────────────────

const (
	oauthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	oauthClientSecret = "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"
	oauthAuthEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	oauthTokenURL     = "https://oauth2.googleapis.com/token"
	oauthUserInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"
	oauthRedirectURI  = "http://localhost:51121/oauth-callback"
	oauthCallbackPort = 51121

	loadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	onboardUserURL    = "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"
)

var oauthScopes = strings.Join([]string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}, " ")

// ─── OAuth URL Generation ────────────────────────────────────────

// GenerateOAuthState creates a 32-char hex random state string.
func GenerateOAuthState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// BuildOAuthURL constructs the Google OAuth2 authorization URL.
func BuildOAuthURL(state string) string {
	params := url.Values{
		"access_type":   {"offline"},
		"client_id":     {oauthClientID},
		"prompt":        {"consent"},
		"redirect_uri":  {oauthRedirectURI},
		"response_type": {"code"},
		"scope":         {oauthScopes},
		"state":         {state},
	}
	return oauthAuthEndpoint + "?" + params.Encode()
}

// ─── Token Exchange ──────────────────────────────────────────────

type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// ExchangeCodeForTokens exchanges an authorization code for tokens.
func ExchangeCodeForTokens(code string) (*OAuthTokens, error) {
	data := url.Values{
		"code":          {code},
		"client_id":     {oauthClientID},
		"client_secret": {oauthClientSecret},
		"redirect_uri":  {oauthRedirectURI},
		"grant_type":    {"authorization_code"},
	}

	resp, err := getOAuthClient().PostForm(oauthTokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokens OAuthTokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parse token response failed: %w", err)
	}

	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("token exchange returned empty access_token")
	}

	return &tokens, nil
}

// ─── User Info ───────────────────────────────────────────────────

// FetchUserEmail fetches the email address associated with an access token.
func FetchUserEmail(accessToken string) (string, error) {
	req, err := http.NewRequest("GET", oauthUserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := getOAuthClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("parse userinfo failed: %w", err)
	}

	return info.Email, nil
}

// ─── Project ID ──────────────────────────────────────────────────

// FetchProjectID gets the GCP project ID via loadCodeAssist / onboardUser.
func FetchProjectID(accessToken string) (string, error) {
	headers := map[string]string{
		"Authorization":    "Bearer " + accessToken,
		"Content-Type":     "application/json",
		"User-Agent":       "google-api-nodejs-client/9.15.1",
		"X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
		"Client-Metadata":  `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`,
	}

	metadata := map[string]interface{}{
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}
	metadataBytes, _ := json.Marshal(metadata)

	// Step 1: loadCodeAssist
	req, err := http.NewRequest("POST", loadCodeAssistURL, strings.NewReader(string(metadataBytes)))
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := getOAuthClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("loadCodeAssist failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var loadResp map[string]interface{}
	json.Unmarshal(body, &loadResp)

	// Try to extract project ID
	if pid := extractProjectID(loadResp); pid != "" {
		return pid, nil
	}

	// Step 2: onboardUser (poll up to 5 times)
	tierID := "legacy-tier"
	if tiers, ok := loadResp["allowedTiers"].([]interface{}); ok {
		for _, t := range tiers {
			if tm, ok := t.(map[string]interface{}); ok {
				if isDefault, ok := tm["isDefault"].(bool); ok && isDefault {
					if id, ok := tm["id"].(string); ok {
						tierID = id
					}
				}
			}
		}
	}

	onboardPayload := map[string]interface{}{
		"tierId": tierID,
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}
	onboardBytes, _ := json.Marshal(onboardPayload)

	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}

		req, err := http.NewRequest("POST", onboardUserURL, strings.NewReader(string(onboardBytes)))
		if err != nil {
			continue
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := getOAuthClient().Do(req)
		if err != nil {
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var onboardResp map[string]interface{}
		json.Unmarshal(body, &onboardResp)

		if done, ok := onboardResp["done"].(bool); ok && done {
			if respData, ok := onboardResp["response"].(map[string]interface{}); ok {
				if pid := extractProjectID(respData); pid != "" {
					return pid, nil
				}
			}
		}
	}

	// Fallback: generate random project-like ID
	b := make([]byte, 5)
	rand.Read(b)
	return fmt.Sprintf("gen-lang-client-%s", hex.EncodeToString(b)), nil
}

func extractProjectID(data map[string]interface{}) string {
	if pid, ok := data["cloudaicompanionProject"].(string); ok && pid != "" {
		return pid
	}
	if pidObj, ok := data["cloudaicompanionProject"].(map[string]interface{}); ok {
		if id, ok := pidObj["id"].(string); ok && id != "" {
			return id
		}
	}
	return ""
}

// ─── Save Auth File ──────────────────────────────────────────────

type AntigravityCredential struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Timestamp    int64  `json:"timestamp"`
	Expired      string `json:"expired"`
	Email        string `json:"email"`
	ProjectID    string `json:"project_id"`
	Disabled     bool   `json:"disabled"`
}

// SaveAuthFile saves the credential to the CPA auths directory.
func SaveAuthFile(tokens *OAuthTokens, email, projectID string) error {
	cfg := config.Get()

	// Determine the auths directory from CPA API URL
	// Default: write to ./auths/ relative to CPA
	authDir := "auths"

	// If CPA API URL is configured, try to use its directory
	if cfg.CPAAPIURL != "" {
		// We'll save in the local auths/ directory
		authDir = "auths"
	}

	if err := os.MkdirAll(authDir, 0755); err != nil {
		return fmt.Errorf("create auth dir failed: %w", err)
	}

	now := time.Now()
	expiry := now.Add(time.Duration(tokens.ExpiresIn) * time.Second)

	cred := AntigravityCredential{
		Type:         "antigravity",
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		Timestamp:    now.UnixMilli(),
		Expired:      expiry.UTC().Format("2006-01-02T15:04:05Z"),
		Email:        email,
		ProjectID:    projectID,
		Disabled:     false,
	}

	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}

	filename := "antigravity.json"
	if email != "" {
		filename = fmt.Sprintf("antigravity-%s.json", email)
	}

	path := filepath.Join(authDir, filename)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write auth file failed: %w", err)
	}

	log.Printf("[%s] 凭证文件已保存: %s", email, path)
	return nil
}

// UploadAuthFileToCPA uploads a credential JSON file to CPA via multipart form upload.
func UploadAuthFileToCPA(jsonData []byte, filename string) error {
	cfg := config.Get()
	if cfg.CPAAPIURL == "" {
		return fmt.Errorf("CPA API URL 未配置")
	}

	apiURL := fmt.Sprintf("%s/v0/management/auth-files", cfg.CPAAPIURL)

	// Build multipart body
	boundary := fmt.Sprintf("----GoFormBoundary%d", time.Now().UnixNano())
	var body strings.Builder
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\n", filename))
	body.WriteString("Content-Type: application/json\r\n\r\n")
	body.Write(jsonData)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(body.String()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.CPAToken))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", cfg.CPAAPIURL)
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", cfg.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("上传请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("解析上传响应失败: %w (body: %s)", err, string(respBody))
	}

	if result.Status != "ok" {
		return fmt.Errorf("上传失败: %s", string(respBody))
	}

	return nil
}

// ─── Complete OAuth Flow ─────────────────────────────────────────

// CompleteOAuthFlow takes an authorization code, exchanges it for tokens,
// fetches user info and project ID, and saves the credential file.
// Returns the email address on success.
func CompleteOAuthFlow(code string) (string, error) {
	// Step 1: Exchange code for tokens
	tokens, err := ExchangeCodeForTokens(code)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}

	// Step 2: Fetch user email
	email, err := FetchUserEmail(tokens.AccessToken)
	if err != nil {
		log.Printf("Warning: fetch user email failed: %v", err)
		email = ""
	}

	// Step 3: Fetch project ID
	projectID, err := FetchProjectID(tokens.AccessToken)
	if err != nil {
		log.Printf("Warning: fetch project ID failed: %v", err)
		projectID = ""
	}

	// Step 4: Save credential file locally
	if err := SaveAuthFile(tokens, email, projectID); err != nil {
		return email, fmt.Errorf("save auth file: %w", err)
	}

	// Step 5: Upload credential to CPA
	now := time.Now()
	expiry := now.Add(time.Duration(tokens.ExpiresIn) * time.Second)
	cred := AntigravityCredential{
		Type:         "antigravity",
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		Timestamp:    now.UnixMilli(),
		Expired:      expiry.UTC().Format("2006-01-02T15:04:05Z"),
		Email:        email,
		ProjectID:    projectID,
		Disabled:     false,
	}
	credJSON, _ := json.Marshal(cred)

	filename := "antigravity.json"
	if email != "" {
		filename = fmt.Sprintf("antigravity-%s.json", email)
	}

	if err := UploadAuthFileToCPA(credJSON, filename); err != nil {
		// Retry up to 3 times
		uploaded := false
		for retry := 1; retry <= 3; retry++ {
			log.Printf("[%s] 凭证上传 CPA 重试 %d/3...", email, retry)
			time.Sleep(3 * time.Second)
			if err := UploadAuthFileToCPA(credJSON, filename); err == nil {
				uploaded = true
				break
			} else {
				log.Printf("[%s] 凭证上传 CPA 重试失败: %v", email, err)
			}
		}
		if !uploaded {
			return email, fmt.Errorf("upload_cpa_failed: 凭证上传 CPA 失败 (已重试3次)")
		}
	}
	log.Printf("[%s] 凭证已上传 CPA", email)

	log.Printf("[%s] OAuth 完成: project=%s", email, projectID)
	return email, nil
}
