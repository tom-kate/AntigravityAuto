package api

import (
	"bytes"
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

// httpClient is a shared client with timeout for all CPA API calls.
var httpClient = &http.Client{Timeout: 30 * time.Second}

type AuthURLResponse struct {
	State  string `json:"state"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

type CallbackResponse struct {
	Status string `json:"status"`
}

func GetOAuthURL() (string, error) {
	url := fmt.Sprintf("%s/v0/management/antigravity-auth-url?is_webui=true", config.App.CPAAPIURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.App.CPAToken))
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", config.App.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var authResp AuthURLResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return "", err
	}

	if authResp.Status != "ok" {
		return "", fmt.Errorf("failed to get oauth url: status %s", authResp.Status)
	}

	return authResp.URL, nil
}

// ─── OAuth Callback Queue ────────────────────────────────────────
// Serializes all OAuth callbacks through a single goroutine to avoid
// CPA server concurrency issues. Each caller submits a request and
// waits for the result.

type callbackRequest struct {
	redirectURL string
	result      chan error
}

var (
	callbackQueue     chan callbackRequest
	callbackQueueOnce sync.Once
)

func initCallbackQueue() {
	callbackQueueOnce.Do(func() {
		callbackQueue = make(chan callbackRequest, 100)
		go callbackWorker()
	})
}

func callbackWorker() {
	for req := range callbackQueue {
		// Process one callback at a time with retry
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			err := sendOAuthCallbackOnce(req.redirectURL)
			if err == nil {
				lastErr = nil
				break
			}
			lastErr = err
			log.Printf("OAuth Callback 重试 %d/3: %v", attempt, err)
			if attempt < 3 {
				time.Sleep(5 * time.Second)
			}
		}
		// Wait a bit before processing the next one
		time.Sleep(1 * time.Second)
		req.result <- lastErr
	}
}

// SendOAuthCallback submits a callback to the queue and waits for the result.
// This ensures callbacks are processed one at a time.
func SendOAuthCallback(redirectURL string) error {
	initCallbackQueue()

	req := callbackRequest{
		redirectURL: redirectURL,
		result:      make(chan error, 1),
	}
	callbackQueue <- req
	return <-req.result
}

// sendOAuthCallbackOnce does a single callback attempt.
func sendOAuthCallbackOnce(redirectURL string) error {
	url := fmt.Sprintf("%s/v0/management/oauth-callback", config.App.CPAAPIURL)

	payload := map[string]interface{}{
		"provider":     "antigravity",
		"redirect_url": redirectURL,
	}

	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.App.CPAToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", config.App.CPAAPIURL)
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", config.App.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var cbResp CallbackResponse
	if err := json.Unmarshal(body, &cbResp); err != nil {
		return fmt.Errorf("parse callback response failed: %w (body: %s)", err, string(body))
	}

	if cbResp.Status != "ok" {
		return fmt.Errorf("callback status: %s (body: %s)", cbResp.Status, string(body))
	}

	return nil
}

// AuthFile represents a single CPA auth file entry
type AuthFile struct {
	Account       string `json:"account"`
	AccountType   string `json:"account_type"`
	AuthIndex     string `json:"auth_index"`
	CreatedAt     string `json:"created_at"`
	Disabled      bool   `json:"disabled"`
	Email         string `json:"email"`
	ID            string `json:"id"`
	Label         string `json:"label"`
	Modtime       string `json:"modtime"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Status        string `json:"status"`
	StatusMessage string `json:"status_message"`
	UpdatedAt     string `json:"updated_at"`
	Unavailable   bool   `json:"unavailable"`
}

// AuthFilesResponse is the response from /v0/management/auth-files
type AuthFilesResponse struct {
	Files []AuthFile `json:"files"`
}

// GetValidationURL polls CPA auth-files API up to maxPolls times (every 5s),
// looking for the account's entry. If status_message contains a 403 error
// with a validation URL, returns that URL. Returns "" if no validation needed.
func GetValidationURL(email string, maxPolls int) (string, error) {
	for i := 0; i < maxPolls; i++ {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		status, url, err := CheckCPAOnce(email)
		if err != nil {
			log.Printf("[%s] CPA poll %d/%d error: %v", email, i+1, maxPolls, err)
			continue
		}
		if url != "" {
			return url, nil
		}
		if status == "not_found" {
			// Account not in CPA yet, keep polling
			continue
		}
		// status == "active" or "error_no_url" — account exists but no validation URL
		// Return empty to let caller decide
		if status == "active" || status == "error_no_url" {
			log.Printf("[%s] CPA status=%s after %d polls, no validation URL", email, status, i+1)
			return "", nil
		}
	}
	return "", nil
}

// CPAStatus represents the result of a single CPA check.
// Possible values: "not_found", "active", "has_url", "error_no_url"
type CPAStatus string

const (
	CPANotFound   CPAStatus = "not_found"    // account not in CPA
	CPAActive     CPAStatus = "active"       // account exists, no error/message yet
	CPAHasURL     CPAStatus = "has_url"      // needs phone binding, URL available
	CPAErrorNoURL CPAStatus = "error_no_url" // has error but no validation URL (dead/quota/etc)
)

// CheckCPAOnce does a single CPA status check. Returns (status, validationURL, error).
func CheckCPAOnce(email string) (CPAStatus, string, error) {
	apiURL := fmt.Sprintf("%s/v0/management/auth-files", config.App.CPAAPIURL)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.App.CPAToken))
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", config.App.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var filesResp AuthFilesResponse
	if err := json.Unmarshal(body, &filesResp); err != nil {
		return "", "", fmt.Errorf("parse failed: %w", err)
	}

	for _, f := range filesResp.Files {
		if !strings.EqualFold(f.Account, email) && !strings.EqualFold(f.Email, email) {
			continue
		}

		log.Printf("[%s] CPA auth-files found, status: %s", email, f.Status)

		if f.StatusMessage == "" {
			return CPAActive, "", nil
		}

		validationURL := parseValidationURL(f.StatusMessage)
		if validationURL != "" {
			return CPAHasURL, validationURL, nil
		}

		return CPAErrorNoURL, "", nil
	}

	return CPANotFound, "", nil
}

// parseValidationURL tries to extract validation URL from status_message JSON
// Looks for error.code==403 and error.details[].links[].url containing "accounts.google.com/signin/continue"
func parseValidationURL(statusMsg string) string {
	var errResp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Links []struct {
					URL         string `json:"url"`
					Description string `json:"description"`
				} `json:"links"`
			} `json:"details"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(statusMsg), &errResp); err != nil {
		return ""
	}

	if errResp.Error.Code != 403 {
		return ""
	}

	// Look for the validation URL in details links
	for _, detail := range errResp.Error.Details {
		for _, link := range detail.Links {
			if strings.Contains(link.URL, "accounts.google.com/signin/continue") {
				return link.URL
			}
		}
	}

	return ""
}

// DeleteAuthFile deletes a CPA auth file by email
// DELETE /v0/management/auth-files?name=antigravity-{email}.json
func DeleteAuthFile(email string) error {
	name := fmt.Sprintf("antigravity-%s.json", strings.ToLower(email))
	apiURL := fmt.Sprintf("%s/v0/management/auth-files?name=%s", config.App.CPAAPIURL, url.QueryEscape(name))

	req, err := http.NewRequest("DELETE", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.App.CPAToken))
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", config.App.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete auth file failed (%d): %s", resp.StatusCode, string(body))
	}

	log.Printf("[%s] CPA auth file deleted: %s", email, name)
	return nil
}

// GetAuthFiles fetches all CPA auth files (single call, no polling)
func GetAuthFiles() ([]AuthFile, error) {
	apiURL := fmt.Sprintf("%s/v0/management/auth-files", config.App.CPAAPIURL)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.App.CPAToken))
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", config.App.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var filesResp AuthFilesResponse
	if err := json.Unmarshal(body, &filesResp); err != nil {
		return nil, fmt.Errorf("parse auth-files failed: %v", err)
	}

	return filesResp.Files, nil
}

// QuotaInfo holds parsed quota for a single model family
type QuotaInfo struct {
	Name              string  `json:"name"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time"`
}

// GetQuota fetches available models via CPA api-call proxy and returns grouped quota
func GetQuota(authIndex string) ([]QuotaInfo, error) {
	apiURL := fmt.Sprintf("%s/v0/management/api-call", config.App.CPAAPIURL)

	payload := map[string]interface{}{
		"authIndex": authIndex,
		"method":    "POST",
		"url":       "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"header": map[string]string{
			"Authorization": "Bearer $TOKEN$",
			"Content-Type":  "application/json",
			"User-Agent":    "antigravity/1.11.5 windows/amd64",
		},
		"data": `{"project":"mercurial-memento-hm6gv"}`,
	}

	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.App.CPAToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", config.App.CPAAPIURL)
	req.Header.Set("Referer", fmt.Sprintf("%s/management.html", config.App.CPAAPIURL))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Parse outer response
	var outer struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(body, &outer); err != nil {
		return nil, fmt.Errorf("parse outer response: %v", err)
	}
	if outer.StatusCode != 200 {
		return nil, fmt.Errorf("api returned status %d", outer.StatusCode)
	}

	// Parse inner body (models)
	var inner struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			QuotaInfo   *struct {
				RemainingFraction float64 `json:"remainingFraction"`
				ResetTime         string  `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(outer.Body), &inner); err != nil {
		return nil, fmt.Errorf("parse models body: %v", err)
	}

	// Group: Pro, Claude, Flash — pick min remaining per group
	type group struct {
		name    string
		prefix  []string
		best    float64
		reset   string
		found   bool
	}
	groups := []group{
		{name: "Pro", prefix: []string{"gemini-3.1-pro", "gemini-3-pro", "gemini-2.5-pro"}},
		{name: "Flash", prefix: []string{"gemini-3-flash", "gemini-3.1-flash", "gemini-2.5-flash"}},
		{name: "Claude", prefix: []string{"claude-"}},
	}

	for i := range groups {
		groups[i].best = -1
	}

	for modelID, m := range inner.Models {
		if m.QuotaInfo == nil || m.DisplayName == "" {
			continue
		}
		for i := range groups {
			for _, pfx := range groups[i].prefix {
				if strings.HasPrefix(modelID, pfx) {
					if !groups[i].found || m.QuotaInfo.RemainingFraction < groups[i].best {
						groups[i].best = m.QuotaInfo.RemainingFraction
						groups[i].reset = m.QuotaInfo.ResetTime
						groups[i].found = true
					}
					break
				}
			}
		}
	}

	var result []QuotaInfo
	for _, g := range groups {
		if g.found {
			result = append(result, QuotaInfo{Name: g.name, RemainingFraction: g.best, ResetTime: g.reset})
		}
	}

	return result, nil
}
