package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"antiauto/api"
	"antiauto/automation"
	"antiauto/config"
	"antiauto/db"

	"github.com/pquerna/otp/totp"
)

//go:embed static
var staticFiles embed.FS

func Start(port int) error {
	staticSub, _ := fs.Sub(staticFiles, "static")
	http.Handle("/", http.FileServer(http.FS(staticSub)))

	http.HandleFunc("/api/masters", handleMasters)
	http.HandleFunc("/api/master/", handleMaster)
	http.HandleFunc("/api/batches", handleBatches)
	http.HandleFunc("/api/batch/", handleBatch)
	http.HandleFunc("/api/cpa-files", handleCPAFiles)
	http.HandleFunc("/api/cpa-quota", handleCPAQuota)
	http.HandleFunc("/api/sms-balance", handleSMSBalance)
	http.HandleFunc("/api/sms-countries", handleSMSCountries)
	http.HandleFunc("/api/sms-services", handleSMSServices)
	http.HandleFunc("/api/sms-prices", handleSMSPrices)
	http.HandleFunc("/api/sms-test", handleSMSTest)
	http.HandleFunc("/api/export", handleExport)
	http.HandleFunc("/api/import", handleImport)
	http.HandleFunc("/api/config", handleConfig)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Web server starting at http://localhost%s", addr)
	return http.ListenAndServe(addr, nil)
}

// ─── Existing handlers (unchanged) ──────────────────────────────────

func handleMasters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		masters := db.DB.GetAllMasters()
		type masterInfo struct {
			db.MasterAccount
			UsedSubs int `json:"used_subs"`
		}
		out := make([]masterInfo, len(masters))
		for i, m := range masters {
			out[i] = masterInfo{MasterAccount: m, UsedSubs: db.DB.CountSubsByMaster(m.ID)}
		}
		json.NewEncoder(w).Encode(out)
	case "POST":
		body, _ := io.ReadAll(r.Body)
		var m db.MasterAccount
		if err := json.Unmarshal(body, &m); err != nil || m.Email == "" {
			http.Error(w, `{"error":"invalid data"}`, 400)
			return
		}
		id := db.DB.AddMaster(m)
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		http.Error(w, `{"error":"method not allowed"}`, 405)
	}
}

func handleMaster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/master/")
	parts := strings.SplitN(path, "/", 2)
	masterID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "update":
		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Remark        *string `json:"remark"`
			ExpiresAt     *string `json:"expires_at"`
			ClearExpiry   bool    `json:"clear_expiry"`
			WeeklyLimited *bool   `json:"weekly_limited"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, 400)
			return
		}
		var expiry *time.Time
		if req.ExpiresAt != nil && *req.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err == nil {
				expiry = &t
			}
		}
		if db.DB.UpdateMaster(masterID, req.Remark, expiry, req.ClearExpiry, req.WeeklyLimited) {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		} else {
			http.Error(w, `{"error":"not found"}`, 404)
		}
	case "delete":
		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		ok, errMsg := db.DB.DeleteMaster(masterID)
		if ok {
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, errMsg), 400)
		}
	case "2fa":
		m := db.DB.GetMaster(masterID)
		if m == nil {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if m.TwoFALink == "" {
			http.Error(w, `{"error":"no 2fa configured"}`, 400)
			return
		}
		secret := strings.ReplaceAll(m.TwoFALink, " ", "")
		if strings.Contains(secret, "secret=") {
			parts := strings.SplitN(secret, "secret=", 2)
			if len(parts) == 2 {
				sec := parts[1]
				if idx := strings.Index(sec, "&"); idx != -1 {
					sec = sec[:idx]
				}
				secret = sec
			}
		}
		code, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"generate failed: %s"}`, err.Error()), 500)
			return
		}
		remaining := 30 - (time.Now().Unix() % 30)
		json.NewEncoder(w).Encode(map[string]interface{}{"code": code, "remaining": remaining})
	case "import":
		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Accounts []db.SubAccount `json:"accounts"`
		}
		if err := json.Unmarshal(body, &req); err != nil || len(req.Accounts) == 0 {
			http.Error(w, `{"error":"invalid data"}`, 400)
			return
		}
		id, errMsg := db.DB.ImportSubAccounts(masterID, req.Accounts)
		if errMsg != "" {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, errMsg), 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		http.Error(w, `{"error":"unknown action"}`, 400)
	}
}

func handleBatches(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		batches := db.DB.GetAllBatches()
		json.NewEncoder(w).Encode(batches)

	case "POST":
		body, _ := io.ReadAll(r.Body)
		var req struct {
			MasterID    string          `json:"master_id"`
			Accounts    []db.SubAccount `json:"accounts"`
			Concurrency int             `json:"concurrency"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, 400)
			return
		}
		if len(req.Accounts) == 0 {
			http.Error(w, `{"error":"no accounts"}`, 400)
			return
		}
		if req.Concurrency < 1 {
			req.Concurrency = 1
		}
		id, errMsg := db.DB.AddBatch(req.MasterID, req.Accounts, req.Concurrency)
		if errMsg != "" {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, errMsg), 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id})

	default:
		http.Error(w, `{"error":"method not allowed"}`, 405)
	}
}

func handleBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/api/batch/")
	parts := strings.SplitN(path, "/", 4)
	batchID := parts[0]

	if batchID == "" {
		http.Error(w, `{"error":"missing batch id"}`, 400)
		return
	}

	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "start":
		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		batch := db.DB.GetBatch(batchID)
		if batch == nil {
			http.Error(w, `{"error":"batch not found"}`, 404)
			return
		}
		if batch.Status == db.BatchRunning {
			http.Error(w, `{"error":"batch already running"}`, 400)
			return
		}
		go automation.RunBatch(batchID)
		json.NewEncoder(w).Encode(map[string]string{"status": "started"})

	case "delete":
		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		if db.DB.DeleteBatch(batchID) {
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		} else {
			http.Error(w, `{"error":"batch not found"}`, 404)
		}

	case "account":
		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		if len(parts) < 4 {
			http.Error(w, `{"error":"missing account index or action"}`, 400)
			return
		}
		idx, err := strconv.Atoi(parts[2])
		if err != nil {
			http.Error(w, `{"error":"invalid account index"}`, 400)
			return
		}
		subAction := parts[3]
		switch subAction {
		case "success":
			if db.DB.SetSubAccountSuccess(batchID, idx) {
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			} else {
				http.Error(w, `{"error":"not found"}`, 404)
			}
		case "delete":
			if db.DB.DeleteSubAccount(batchID, idx) {
				json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			} else {
				http.Error(w, `{"error":"not found"}`, 404)
			}
		case "bind-phone":
			go automation.RunPhoneBind(batchID, idx)
			json.NewEncoder(w).Encode(map[string]string{"status": "started"})
		case "age-verify":
			go automation.RunAgeVerify(batchID, idx)
			json.NewEncoder(w).Encode(map[string]string{"status": "started"})
		default:
			http.Error(w, `{"error":"unknown sub-action"}`, 400)
		}

	case "":
		if r.Method != "GET" {
			http.Error(w, `{"error":"method not allowed"}`, 405)
			return
		}
		batch := db.DB.GetBatch(batchID)
		if batch == nil {
			http.Error(w, `{"error":"batch not found"}`, 404)
			return
		}
		json.NewEncoder(w).Encode(batch)

	default:
		http.Error(w, `{"error":"unknown action"}`, 400)
	}
}

func handleCPAFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	files, err := api.GetAuthFiles()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	json.NewEncoder(w).Encode(files)
}

func handleCPAQuota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	authIndex := r.URL.Query().Get("auth_index")
	if authIndex == "" {
		http.Error(w, `{"error":"missing auth_index"}`, 400)
		return
	}
	quota, err := api.GetQuota(authIndex)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	json.NewEncoder(w).Encode(quota)
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		cfg := config.Get()
		json.NewEncoder(w).Encode(cfg)

	case "POST":
		body, _ := io.ReadAll(r.Body)
		var cfg config.Config
		if err := json.Unmarshal(body, &cfg); err != nil {
			http.Error(w, `{"error":"invalid json"}`, 400)
			return
		}
		if err := config.Save(cfg); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, `{"error":"method not allowed"}`, 405)
	}
}

func handleSMSBalance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	balance, err := api.GetSMSBalance()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]float64{"balance": balance})
}

func handleSMSCountries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	countries, err := api.GetSMSCountries()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	json.NewEncoder(w).Encode(countries)
}

func handleSMSServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	countryStr := r.URL.Query().Get("country")
	country := 0
	if countryStr != "" {
		country, _ = strconv.Atoi(countryStr)
	}
	services, err := api.GetSMSServices(country)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	json.NewEncoder(w).Encode(services)
}

func handleSMSPrices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	countryStr := r.URL.Query().Get("country")
	service := r.URL.Query().Get("service")
	country := 0
	if countryStr != "" {
		country, _ = strconv.Atoi(countryStr)
	}
	price, err := api.GetSMSPrices(country, service)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	json.NewEncoder(w).Encode(price)
}

func handleSMSTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	phone, activationID, err := api.TestGetNumber()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	// Immediately cancel the test number
	api.ReleasePhone(activationID)
	json.NewEncoder(w).Encode(map[string]string{"phone": phone, "activation_id": activationID, "status": "ok (已自动释放)"})
}

type exportData struct {
	Config  config.Config      `json:"config"`
	Masters []db.MasterAccount `json:"masters"`
	Batches []db.Batch         `json:"batches"`
}

func handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=antiauto-export.json")
	data := exportData{
		Config:  config.Get(),
		Masters: db.DB.GetAllMasters(),
		Batches: db.DB.GetAllBatches(),
	}
	json.NewEncoder(w).Encode(data)
}

func handleImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var data exportData
	if err := json.Unmarshal(body, &data); err != nil {
		http.Error(w, `{"error":"JSON 解析失败"}`, 400)
		return
	}

	imported := 0
	for _, m := range data.Masters {
		if existing := db.DB.GetMaster(m.ID); existing != nil {
			continue
		}
		db.DB.AddMasterWithID(m)
		imported++
	}

	batchImported := 0
	for _, b := range data.Batches {
		if existing := db.DB.GetBatch(b.ID); existing != nil {
			continue
		}
		db.DB.AddBatchWithID(b)
		batchImported++
	}

	// Restore config if present
	configRestored := false
	if data.Config.Port != 0 {
		if err := config.Save(data.Config); err == nil {
			configRestored = true
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "ok",
		"masters_imported":  imported,
		"batches_imported":  batchImported,
		"config_restored":   configRestored,
	})
}
