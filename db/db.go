package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type SubAccountStatus string

const (
	StatusPending SubAccountStatus = "pending"
	StatusRunning SubAccountStatus = "running"
	StatusSuccess SubAccountStatus = "success"
	StatusFailed  SubAccountStatus = "failed"
	StatusError   SubAccountStatus = "error"
)

type SubAccount struct {
	Email      string           `json:"email"`
	Password   string           `json:"password"`
	AuxEmail   string           `json:"aux_email"`
	TwoFA      string           `json:"two_fa"`
	Status     SubAccountStatus `json:"status"`
	Retries    int              `json:"retries"`
	Step       string           `json:"step"`
	ErrorLog   []string         `json:"error_log"`
	PhoneBound bool             `json:"phone_bound"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
}

type MasterAccount struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	Password      string     `json:"password"`
	AuxEmail      string     `json:"aux_email"`
	TwoFALink     string     `json:"two_fa_link"`
	Remark        string     `json:"remark"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	WeeklyLimited bool       `json:"weekly_limited"`
}

type BatchStatus string

const (
	BatchPending  BatchStatus = "pending"
	BatchRunning  BatchStatus = "running"
	BatchFinished BatchStatus = "finished"
)

type Batch struct {
	ID          string       `json:"id"`
	MasterID    string       `json:"master_id"`
	Accounts    []SubAccount `json:"accounts"`
	Status      BatchStatus  `json:"status"`
	Concurrency int          `json:"concurrency"`
	CreatedAt   time.Time    `json:"created_at"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
}

const MaxRetries = 3
const MaxSubsPerMaster = 5

type Database struct {
	mu sync.RWMutex
	db *sql.DB
}

var DB *Database

const createTablesSQL = `
CREATE TABLE IF NOT EXISTS masters (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    password TEXT NOT NULL,
    aux_email TEXT DEFAULT '',
    two_fa_link TEXT DEFAULT '',
    remark TEXT DEFAULT '',
    expires_at TEXT,
    weekly_limited INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS batches (
    id TEXT PRIMARY KEY,
    master_id TEXT NOT NULL,
    flow_id TEXT DEFAULT '',
    status TEXT DEFAULT 'pending',
    concurrency INTEGER DEFAULT 1,
    created_at TEXT NOT NULL,
    finished_at TEXT,
    FOREIGN KEY (master_id) REFERENCES masters(id)
);

CREATE TABLE IF NOT EXISTS sub_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL,
    idx INTEGER NOT NULL,
    email TEXT NOT NULL,
    password TEXT NOT NULL,
    aux_email TEXT DEFAULT '',
    two_fa TEXT DEFAULT '',
    status TEXT DEFAULT 'pending',
    retries INTEGER DEFAULT 0,
    step TEXT DEFAULT '',
    error_log TEXT DEFAULT '[]',
    phone_bound INTEGER DEFAULT 0,
    start_from TEXT DEFAULT '',
    finished_at TEXT,
    FOREIGN KEY (batch_id) REFERENCES batches(id) ON DELETE CASCADE
);
`

func Init(dbPath string) error {
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %v", err)
	}

	// SQLite concurrency: single writer, busy timeout to avoid SQLITE_BUSY
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("failed to enable WAL: %v", err)
	}
	if _, err := sqlDB.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return fmt.Errorf("failed to set busy_timeout: %v", err)
	}
	if _, err := sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("failed to enable foreign keys: %v", err)
	}

	// Create tables
	if _, err := sqlDB.Exec(createTablesSQL); err != nil {
		return fmt.Errorf("failed to create tables: %v", err)
	}

	DB = &Database{db: sqlDB}
	return nil
}

// ---------- helpers ----------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseNullableTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func parseErrorLog(s string) []string {
	var out []string
	if s == "" {
		return []string{}
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// scanSubAccounts queries sub_accounts for a given batch_id ordered by idx.
func scanSubAccounts(querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, batchID string) []SubAccount {
	rows, err := querier.Query(
		`SELECT email, password, aux_email, two_fa, status, retries, step, error_log, phone_bound, finished_at
		 FROM sub_accounts WHERE batch_id = ? ORDER BY idx`, batchID)
	if err != nil {
		return []SubAccount{}
	}
	defer rows.Close()
	var accounts []SubAccount
	for rows.Next() {
		var (
			email      string
			password   string
			auxEmail   string
			twoFA      string
			status     string
			retries    int
			step       string
			errorLog   string
			phoneBound int
			finishedAt sql.NullString
		)
		if err := rows.Scan(&email, &password, &auxEmail, &twoFA, &status, &retries, &step, &errorLog, &phoneBound, &finishedAt); err != nil {
			continue
		}
		accounts = append(accounts, SubAccount{
			Email:      email,
			Password:   password,
			AuxEmail:   auxEmail,
			TwoFA:      twoFA,
			Status:     SubAccountStatus(status),
			Retries:    retries,
			Step:       step,
			ErrorLog:   parseErrorLog(errorLog),
			PhoneBound: phoneBound != 0,
			FinishedAt: parseNullableTime(finishedAt),
		})
	}
	if accounts == nil {
		accounts = []SubAccount{}
	}
	return accounts
}

// ---------- MasterAccount methods ----------

// AddMaster adds a new master account, returns its ID
func (d *Database) AddMaster(m MasterAccount) string {
	d.mu.Lock()
	defer d.mu.Unlock()

	m.ID = fmt.Sprintf("m_%d", time.Now().UnixMilli())

	var expiresAt sql.NullString
	if m.ExpiresAt != nil {
		expiresAt = sql.NullString{String: m.ExpiresAt.Format(time.RFC3339), Valid: true}
	}

	d.db.Exec(
		`INSERT INTO masters (id, email, password, aux_email, two_fa_link, remark, expires_at, weekly_limited)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Email, m.Password, m.AuxEmail, m.TwoFALink, m.Remark, expiresAt, boolToInt(m.WeeklyLimited),
	)
	return m.ID
}

// GetAllMasters returns all master accounts
func (d *Database) GetAllMasters() []MasterAccount {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, email, password, aux_email, two_fa_link, remark, expires_at, weekly_limited FROM masters`)
	if err != nil {
		return []MasterAccount{}
	}
	defer rows.Close()

	var result []MasterAccount
	for rows.Next() {
		var (
			id            string
			email         string
			password      string
			auxEmail      string
			twoFALink     string
			remark        string
			expiresAt     sql.NullString
			weeklyLimited int
		)
		if err := rows.Scan(&id, &email, &password, &auxEmail, &twoFALink, &remark, &expiresAt, &weeklyLimited); err != nil {
			continue
		}
		result = append(result, MasterAccount{
			ID:            id,
			Email:         email,
			Password:      password,
			AuxEmail:      auxEmail,
			TwoFALink:     twoFALink,
			Remark:        remark,
			ExpiresAt:     parseNullableTime(expiresAt),
			WeeklyLimited: weeklyLimited != 0,
		})
	}
	if result == nil {
		result = []MasterAccount{}
	}
	return result
}

// GetMaster returns a master by ID
func (d *Database) GetMaster(id string) *MasterAccount {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var (
		mid           string
		email         string
		password      string
		auxEmail      string
		twoFALink     string
		remark        string
		expiresAt     sql.NullString
		weeklyLimited int
	)
	err := d.db.QueryRow(
		`SELECT id, email, password, aux_email, two_fa_link, remark, expires_at, weekly_limited FROM masters WHERE id = ?`, id,
	).Scan(&mid, &email, &password, &auxEmail, &twoFALink, &remark, &expiresAt, &weeklyLimited)
	if err != nil {
		return nil
	}
	return &MasterAccount{
		ID:            mid,
		Email:         email,
		Password:      password,
		AuxEmail:      auxEmail,
		TwoFALink:     twoFALink,
		Remark:        remark,
		ExpiresAt:     parseNullableTime(expiresAt),
		WeeklyLimited: weeklyLimited != 0,
	}
}

// UpdateMaster updates mutable fields of a master account
func (d *Database) UpdateMaster(id string, remark *string, expiresAt *time.Time, clearExpiry bool, weeklyLimited *bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check master exists
	var exists int
	if err := d.db.QueryRow(`SELECT 1 FROM masters WHERE id = ?`, id).Scan(&exists); err != nil {
		return false
	}

	if remark != nil {
		d.db.Exec(`UPDATE masters SET remark = ? WHERE id = ?`, *remark, id)
	}
	if clearExpiry {
		d.db.Exec(`UPDATE masters SET expires_at = NULL WHERE id = ?`, id)
	} else if expiresAt != nil {
		d.db.Exec(`UPDATE masters SET expires_at = ? WHERE id = ?`, expiresAt.Format(time.RFC3339), id)
	}
	if weeklyLimited != nil {
		d.db.Exec(`UPDATE masters SET weekly_limited = ? WHERE id = ?`, boolToInt(*weeklyLimited), id)
	}
	return true
}

// DeleteMaster removes a master account (only if no batches reference it)
func (d *Database) DeleteMaster(id string) (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check for batches referencing this master
	var batchCount int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM batches WHERE master_id = ?`, id).Scan(&batchCount); err == nil && batchCount > 0 {
		return false, "该母号下还有批次，无法删除"
	}

	res, err := d.db.Exec(`DELETE FROM masters WHERE id = ?`, id)
	if err != nil {
		return false, "母号不存在"
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return false, "母号不存在"
	}
	return true, ""
}

// countSubsByMaster counts total child accounts belonging to a master across all batches (caller must hold lock)
func (d *Database) countSubsByMaster(masterID string) int {
	var count int
	d.db.QueryRow(
		`SELECT COUNT(*) FROM sub_accounts sa JOIN batches b ON sa.batch_id = b.id WHERE b.master_id = ?`, masterID,
	).Scan(&count)
	return count
}

// CountSubsByMaster is the exported thread-safe version
func (d *Database) CountSubsByMaster(masterID string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.countSubsByMaster(masterID)
}

// ImportSubAccounts adds already-configured sub-accounts (status=success, no automation needed)
func (d *Database) ImportSubAccounts(masterID string, accounts []SubAccount) (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check master exists
	var exists int
	if err := d.db.QueryRow(`SELECT 1 FROM masters WHERE id = ?`, masterID).Scan(&exists); err != nil {
		return "", "母号不存在"
	}

	used := d.countSubsByMaster(masterID)
	remain := MaxSubsPerMaster - used
	if remain <= 0 {
		return "", "该母号已达5个子号上限"
	}
	if len(accounts) > remain {
		return "", fmt.Sprintf("该母号还能添加%d个子号，本次提交了%d个", remain, len(accounts))
	}

	now := time.Now()
	nowStr := now.Format(time.RFC3339)
	id := fmt.Sprintf("batch_%d", now.UnixMilli())

	tx, err := d.db.Begin()
	if err != nil {
		return "", "数据库错误"
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO batches (id, master_id, flow_id, status, concurrency, created_at, finished_at)
		 VALUES (?, ?, '', ?, 1, ?, ?)`,
		id, masterID, string(BatchFinished), nowStr, nowStr,
	)
	if err != nil {
		return "", "数据库错误"
	}

	for idx, acc := range accounts {
		errLogJSON, _ := json.Marshal([]string{})
		_, err = tx.Exec(
			`INSERT INTO sub_accounts (batch_id, idx, email, password, aux_email, two_fa, status, retries, step, error_log, phone_bound, start_from, finished_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 0, 'done', ?, ?, '', ?)`,
			id, idx, acc.Email, acc.Password, acc.AuxEmail, acc.TwoFA,
			string(StatusSuccess), string(errLogJSON), boolToInt(acc.PhoneBound), nowStr,
		)
		if err != nil {
			return "", "数据库错误"
		}
	}

	if err := tx.Commit(); err != nil {
		return "", "数据库错误"
	}
	return id, ""
}

// AddBatch creates a new batch under a master (total subs per master <= 5)
func (d *Database) AddBatch(masterID string, accounts []SubAccount, concurrency int) (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check master exists
	var exists int
	if err := d.db.QueryRow(`SELECT 1 FROM masters WHERE id = ?`, masterID).Scan(&exists); err != nil {
		return "", "母号不存在"
	}

	used := d.countSubsByMaster(masterID)
	remain := MaxSubsPerMaster - used
	if remain <= 0 {
		return "", "该母号已达5个子号上限"
	}
	if len(accounts) > remain {
		return "", fmt.Sprintf("该母号还能添加%d个子号，本次提交了%d个", remain, len(accounts))
	}

	now := time.Now()
	id := fmt.Sprintf("batch_%d", now.UnixMilli())
	if concurrency < 1 {
		concurrency = 1
	}

	tx, err := d.db.Begin()
	if err != nil {
		return "", "数据库错误"
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO batches (id, master_id, flow_id, status, concurrency, created_at)
		 VALUES (?, ?, '', ?, ?, ?)`,
		id, masterID, string(BatchPending), concurrency, now.Format(time.RFC3339),
	)
	if err != nil {
		return "", "数据库错误"
	}

	for idx, acc := range accounts {
		errLogJSON, _ := json.Marshal([]string{})
		_, err = tx.Exec(
			`INSERT INTO sub_accounts (batch_id, idx, email, password, aux_email, two_fa, status, retries, step, error_log, phone_bound, start_from)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', ?, 0, '')`,
			id, idx, acc.Email, acc.Password, acc.AuxEmail, acc.TwoFA,
			string(StatusPending), string(errLogJSON),
		)
		if err != nil {
			return "", "数据库错误"
		}
	}

	if err := tx.Commit(); err != nil {
		return "", "数据库错误"
	}
	return id, ""
}

func (d *Database) GetBatch(id string) *Batch {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var (
		bid         string
		masterID    string
		status      string
		concurrency int
		createdAt   string
		finishedAt  sql.NullString
	)
	err := d.db.QueryRow(
		`SELECT id, master_id, status, concurrency, created_at, finished_at FROM batches WHERE id = ?`, id,
	).Scan(&bid, &masterID, &status, &concurrency, &createdAt, &finishedAt)
	if err != nil {
		return nil
	}

	ca, _ := time.Parse(time.RFC3339, createdAt)
	b := &Batch{
		ID:          bid,
		MasterID:    masterID,
		Status:      BatchStatus(status),
		Concurrency: concurrency,
		CreatedAt:   ca,
		FinishedAt:  parseNullableTime(finishedAt),
		Accounts:    scanSubAccounts(d.db, bid),
	}
	return b
}

func (d *Database) GetAllBatches() []Batch {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, master_id, status, concurrency, created_at, finished_at FROM batches`)
	if err != nil {
		return []Batch{}
	}
	defer rows.Close()

	var result []Batch
	for rows.Next() {
		var (
			bid         string
			masterID    string
			status      string
			concurrency int
			createdAt   string
			finishedAt  sql.NullString
		)
		if err := rows.Scan(&bid, &masterID, &status, &concurrency, &createdAt, &finishedAt); err != nil {
			continue
		}
		ca, _ := time.Parse(time.RFC3339, createdAt)
		result = append(result, Batch{
			ID:          bid,
			MasterID:    masterID,
			Status:      BatchStatus(status),
			Concurrency: concurrency,
			CreatedAt:   ca,
			FinishedAt:  parseNullableTime(finishedAt),
		})
	}

	// Populate accounts for each batch
	for i := range result {
		result[i].Accounts = scanSubAccounts(d.db, result[i].ID)
	}

	if result == nil {
		result = []Batch{}
	}
	return result
}

func (d *Database) UpdateBatchStatus(batchID string, status BatchStatus) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if status == BatchFinished {
		now := time.Now().Format(time.RFC3339)
		d.db.Exec(`UPDATE batches SET status = ?, finished_at = ? WHERE id = ?`, string(status), now, batchID)
	} else {
		d.db.Exec(`UPDATE batches SET status = ? WHERE id = ?`, string(status), batchID)
	}
}

func (d *Database) UpdateSubAccount(batchID string, idx int, status SubAccountStatus, step string, errMsg string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Read current error_log and retries
	var currentErrLog string
	var currentRetries int
	err := d.db.QueryRow(
		`SELECT error_log, retries FROM sub_accounts WHERE batch_id = ? AND idx = ?`, batchID, idx,
	).Scan(&currentErrLog, &currentRetries)
	if err != nil {
		return
	}

	errLog := parseErrorLog(currentErrLog)
	if errMsg != "" {
		errLog = append(errLog, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), errMsg))
	}
	errLogJSON, _ := json.Marshal(errLog)

	retries := currentRetries
	if status == StatusFailed || status == StatusError {
		retries++
	}

	var finishedAt sql.NullString
	if status == StatusSuccess || status == StatusFailed || status == StatusError {
		finishedAt = sql.NullString{String: time.Now().Format(time.RFC3339), Valid: true}
	}

	d.db.Exec(
		`UPDATE sub_accounts SET status = ?, step = ?, error_log = ?, retries = ?, finished_at = COALESCE(?, finished_at) WHERE batch_id = ? AND idx = ?`,
		string(status), step, string(errLogJSON), retries, finishedAt, batchID, idx,
	)
}

func (d *Database) SetPhoneBound(batchID string, idx int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`UPDATE sub_accounts SET phone_bound = 1 WHERE batch_id = ? AND idx = ?`, batchID, idx)
}

// DeleteSubAccount removes a sub-account from a batch by index
func (d *Database) DeleteSubAccount(batchID string, idx int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check the sub_account exists
	var exists int
	if err := d.db.QueryRow(`SELECT 1 FROM sub_accounts WHERE batch_id = ? AND idx = ?`, batchID, idx).Scan(&exists); err != nil {
		return false
	}

	tx, err := d.db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()

	// Delete the sub_account
	tx.Exec(`DELETE FROM sub_accounts WHERE batch_id = ? AND idx = ?`, batchID, idx)

	// Re-index remaining sub_accounts: shift all indices above the deleted one down by 1
	tx.Exec(`UPDATE sub_accounts SET idx = idx - 1 WHERE batch_id = ? AND idx > ?`, batchID, idx)

	// Check if batch has any accounts left
	var remaining int
	tx.QueryRow(`SELECT COUNT(*) FROM sub_accounts WHERE batch_id = ?`, batchID).Scan(&remaining)
	if remaining == 0 {
		tx.Exec(`DELETE FROM batches WHERE id = ?`, batchID)
	}

	if err := tx.Commit(); err != nil {
		return false
	}
	return true
}

// SetSubAccountSuccess manually marks a sub-account as success
func (d *Database) SetSubAccountSuccess(batchID string, idx int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	var exists int
	if err := d.db.QueryRow(`SELECT 1 FROM sub_accounts WHERE batch_id = ? AND idx = ?`, batchID, idx).Scan(&exists); err != nil {
		return false
	}

	now := time.Now().Format(time.RFC3339)
	d.db.Exec(
		`UPDATE sub_accounts SET status = ?, step = 'done', finished_at = ? WHERE batch_id = ? AND idx = ?`,
		string(StatusSuccess), now, batchID, idx,
	)
	return true
}

func (d *Database) DeleteBatch(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Delete sub_accounts first (in case foreign key cascade isn't enough)
	d.db.Exec(`DELETE FROM sub_accounts WHERE batch_id = ?`, id)

	res, err := d.db.Exec(`DELETE FROM batches WHERE id = ?`, id)
	if err != nil {
		return false
	}
	affected, _ := res.RowsAffected()
	return affected > 0
}
