package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	CreativeJobCreated    = "created"
	CreativeJobProcessing = "processing"
	CreativeJobCompleted  = "completed"
	CreativeJobFailed     = "failed"
	CreativeJobRefunded   = "refunded"
)

type CreativeProvider struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	BaseURL     string    `json:"base_url"`
	APIKey      string    `json:"-"`
	SourceGroup string    `json:"source_group"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type CreativeUserCredential struct {
	UserID       int64     `json:"user_id"`
	ProviderID   int64     `json:"provider_id"`
	APIKeyCipher string    `json:"-"`
	KeyHint      string    `json:"key_hint"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type CreativeModel struct {
	ID                int64     `json:"id"`
	ProviderID        int64     `json:"provider_id"`
	ModelID           string    `json:"model_id"`
	DisplayName       string    `json:"display_name"`
	Capability        string    `json:"capability"`
	Protocol          string    `json:"protocol"`
	PriceJSON         string    `json:"price_json"`
	ConstraintsJSON   string    `json:"constraints_json"`
	SourceGroup       string    `json:"source_group"`
	Enabled           bool      `json:"enabled"`
	AvailableAccounts int       `json:"available_accounts"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type CreativeJob struct {
	ID                int64      `json:"id"`
	OrderNo           string     `json:"order_no"`
	RequestKey        string     `json:"request_key"`
	UserID            int64      `json:"user_id"`
	ProviderID        int64      `json:"provider_id"`
	ModelID           string     `json:"model_id"`
	MediaType         string     `json:"media_type"`
	Prompt            string     `json:"prompt"`
	ParamsJSON        string     `json:"params_json"`
	UpstreamRequestID string     `json:"upstream_request_id"`
	UpstreamStatus    string     `json:"upstream_status"`
	ResultJSON        string     `json:"result_json"`
	ChargeAmount      float64    `json:"charge_amount"`
	ChargeStatus      string     `json:"charge_status"`
	ChargeLedgerID    int64      `json:"charge_ledger_id"`
	RefundLedgerID    int64      `json:"refund_ledger_id"`
	Status            string     `json:"status"`
	Progress          int        `json:"progress"`
	ErrorCode         string     `json:"error_code"`
	ErrorMessage      string     `json:"error_message"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	DeletedAt         *time.Time `json:"deleted_at,omitempty"`
}
type CreativeJobFilter struct {
	UserID         int64
	MediaType      string
	Status         string
	Limit          int
	Offset         int
	IncludeDeleted bool
}
type CreativeJobEvent struct {
	ID        int64     `json:"id"`
	JobID     int64     `json:"job_id"`
	EventType string    `json:"event_type"`
	Message   string    `json:"message"`
	DataJSON  string    `json:"data_json"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) ensureCreativeSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS creative_providers (id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,kind TEXT NOT NULL DEFAULT 'openai_compatible',base_url TEXT NOT NULL,api_key TEXT NOT NULL DEFAULT '',source_group TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS creative_models (id INTEGER PRIMARY KEY AUTOINCREMENT,provider_id INTEGER NOT NULL,model_id TEXT NOT NULL,display_name TEXT NOT NULL DEFAULT '',capability TEXT NOT NULL DEFAULT 'unknown',protocol TEXT NOT NULL DEFAULT '',price_json TEXT NOT NULL DEFAULT '{}',constraints_json TEXT NOT NULL DEFAULT '{}',source_group TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,FOREIGN KEY(provider_id) REFERENCES creative_providers(id) ON DELETE CASCADE,UNIQUE(provider_id,model_id));
CREATE INDEX IF NOT EXISTS idx_creative_models_provider ON creative_models(provider_id,enabled,capability);
CREATE TABLE IF NOT EXISTS creative_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT,order_no TEXT NOT NULL UNIQUE,request_key TEXT NOT NULL,user_id INTEGER NOT NULL,provider_id INTEGER NOT NULL,model_id TEXT NOT NULL,media_type TEXT NOT NULL,prompt TEXT NOT NULL DEFAULT '',params_json TEXT NOT NULL DEFAULT '{}',upstream_request_id TEXT NOT NULL DEFAULT '',upstream_status TEXT NOT NULL DEFAULT '',result_json TEXT NOT NULL DEFAULT '{}',charge_amount REAL NOT NULL DEFAULT 0,charge_status TEXT NOT NULL DEFAULT '',charge_ledger_id INTEGER NOT NULL DEFAULT 0,refund_ledger_id INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL,progress INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '',error_message TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',FOREIGN KEY(provider_id) REFERENCES creative_providers(id),UNIQUE(user_id,request_key));
CREATE INDEX IF NOT EXISTS idx_creative_jobs_user ON creative_jobs(user_id,created_at DESC);
CREATE INDEX IF NOT EXISTS idx_creative_jobs_status ON creative_jobs(status,updated_at);
CREATE TABLE IF NOT EXISTS creative_job_events (id INTEGER PRIMARY KEY AUTOINCREMENT,job_id INTEGER NOT NULL,event_type TEXT NOT NULL,message TEXT NOT NULL DEFAULT '',data_json TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL,FOREIGN KEY(job_id) REFERENCES creative_jobs(id) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS idx_creative_events_job ON creative_job_events(job_id,created_at);
CREATE TABLE IF NOT EXISTS creative_user_credentials (user_id INTEGER NOT NULL,provider_id INTEGER NOT NULL,api_key_cipher TEXT NOT NULL,key_hint TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(user_id,provider_id),FOREIGN KEY(provider_id) REFERENCES creative_providers(id) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS idx_creative_user_credentials_provider ON creative_user_credentials(provider_id,user_id);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE creative_jobs ADD COLUMN deleted_at TEXT NOT NULL DEFAULT ''`)
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_creative_jobs_deleted ON creative_jobs(deleted_at,user_id,created_at DESC)`)
	return err
}

func (s *Store) SaveCreativeProvider(ctx context.Context, p CreativeProvider) (*CreativeProvider, error) {
	now := time.Now().UTC()
	p.Name = strings.TrimSpace(p.Name)
	p.Kind = strings.TrimSpace(p.Kind)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	p.SourceGroup = strings.TrimSpace(p.SourceGroup)
	if p.Name == "" || p.BaseURL == "" {
		return nil, fmt.Errorf("provider name and base_url are required")
	}
	if p.Kind == "" {
		p.Kind = "openai_compatible"
	}
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO creative_providers(name,kind,base_url,api_key,source_group,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, p.Name, p.Kind, p.BaseURL, p.APIKey, p.SourceGroup, boolInt(p.Enabled), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		p.ID, _ = res.LastInsertId()
	} else if strings.TrimSpace(p.APIKey) == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE creative_providers SET name=?,kind=?,base_url=?,source_group=?,enabled=?,updated_at=? WHERE id=?`, p.Name, p.Kind, p.BaseURL, p.SourceGroup, boolInt(p.Enabled), now.Format(time.RFC3339Nano), p.ID)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := s.db.ExecContext(ctx, `UPDATE creative_providers SET name=?,kind=?,base_url=?,api_key=?,source_group=?,enabled=?,updated_at=? WHERE id=?`, p.Name, p.Kind, p.BaseURL, p.APIKey, p.SourceGroup, boolInt(p.Enabled), now.Format(time.RFC3339Nano), p.ID)
		if err != nil {
			return nil, err
		}
	}
	return s.GetCreativeProvider(ctx, p.ID)
}
func (s *Store) GetCreativeProvider(ctx context.Context, id int64) (*CreativeProvider, error) {
	return scanCreativeProvider(s.db.QueryRowContext(ctx, `SELECT id,name,kind,base_url,api_key,source_group,enabled,created_at,updated_at FROM creative_providers WHERE id=?`, id))
}
func (s *Store) ListCreativeProviders(ctx context.Context) ([]CreativeProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,base_url,api_key,source_group,enabled,created_at,updated_at FROM creative_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreativeProvider{}
	for rows.Next() {
		p, err := scanCreativeProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (s *Store) DeleteCreativeProvider(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM creative_providers WHERE id=?`, id)
	return err
}
func (s *Store) ClearCreativeProviderAPIKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE creative_providers SET api_key='',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}
func scanCreativeProvider(row interface{ Scan(...any) error }) (*CreativeProvider, error) {
	var p CreativeProvider
	var e int
	var c, u string
	if err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.APIKey, &p.SourceGroup, &e, &c, &u); err != nil {
		return nil, err
	}
	p.Enabled = e != 0
	p.CreatedAt = parseStoreTime(c)
	p.UpdatedAt = parseStoreTime(u)
	return &p, nil
}

func (s *Store) SaveCreativeUserCredential(ctx context.Context, value CreativeUserCredential) (*CreativeUserCredential, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO creative_user_credentials(user_id,provider_id,api_key_cipher,key_hint,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id,provider_id) DO UPDATE SET api_key_cipher=excluded.api_key_cipher,key_hint=excluded.key_hint,updated_at=excluded.updated_at`, value.UserID, value.ProviderID, value.APIKeyCipher, value.KeyHint, now, now)
	if err != nil {
		return nil, err
	}
	return s.GetCreativeUserCredential(ctx, value.UserID, value.ProviderID)
}

func (s *Store) GetCreativeUserCredential(ctx context.Context, userID, providerID int64) (*CreativeUserCredential, error) {
	return scanCreativeUserCredential(s.db.QueryRowContext(ctx, `SELECT user_id,provider_id,api_key_cipher,key_hint,created_at,updated_at FROM creative_user_credentials WHERE user_id=? AND provider_id=?`, userID, providerID))
}

func (s *Store) ListCreativeUserCredentials(ctx context.Context, userID int64) ([]CreativeUserCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,provider_id,api_key_cipher,key_hint,created_at,updated_at FROM creative_user_credentials WHERE user_id=? ORDER BY provider_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreativeUserCredential{}
	for rows.Next() {
		value, scanErr := scanCreativeUserCredential(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *value)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCreativeUserCredential(ctx context.Context, userID, providerID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM creative_user_credentials WHERE user_id=? AND provider_id=?`, userID, providerID)
	return err
}

func (s *Store) HasActiveCreativeVideo(ctx context.Context, userID, providerID int64) (bool, error) {
	var exists int
	query := `SELECT EXISTS(SELECT 1 FROM creative_jobs WHERE provider_id=? AND media_type='video' AND status IN ('created','processing')`
	args := []any{providerID}
	if userID > 0 {
		query += ` AND user_id=?`
		args = append(args, userID)
	}
	query += `)`
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&exists)
	return exists != 0, err
}

func (s *Store) HasCreativeJobsForProvider(ctx context.Context, providerID int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM creative_jobs WHERE provider_id=?)`, providerID).Scan(&exists)
	return exists != 0, err
}

func scanCreativeUserCredential(row interface{ Scan(...any) error }) (*CreativeUserCredential, error) {
	var value CreativeUserCredential
	var createdAt, updatedAt string
	if err := row.Scan(&value.UserID, &value.ProviderID, &value.APIKeyCipher, &value.KeyHint, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	value.CreatedAt = parseStoreTime(createdAt)
	value.UpdatedAt = parseStoreTime(updatedAt)
	return &value, nil
}

const creativeModelSelect = `SELECT id,provider_id,model_id,display_name,capability,protocol,price_json,constraints_json,source_group,enabled,created_at,updated_at FROM creative_models`

func (s *Store) UpsertCreativeModel(ctx context.Context, m CreativeModel) (*CreativeModel, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if m.DisplayName == "" {
		m.DisplayName = m.ModelID
	}
	if m.Capability == "" {
		m.Capability = "unknown"
	}
	if m.PriceJSON == "" {
		m.PriceJSON = "{}"
	}
	if m.ConstraintsJSON == "" {
		m.ConstraintsJSON = "{}"
	}
	if m.ID == 0 {
		_, err := s.db.ExecContext(ctx, `INSERT INTO creative_models(provider_id,model_id,display_name,capability,protocol,price_json,constraints_json,source_group,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider_id,model_id) DO UPDATE SET source_group=excluded.source_group,updated_at=excluded.updated_at`, m.ProviderID, m.ModelID, m.DisplayName, m.Capability, m.Protocol, m.PriceJSON, m.ConstraintsJSON, m.SourceGroup, boolInt(m.Enabled), now, now)
		if err != nil {
			return nil, err
		}
		return s.GetCreativeModelByProviderModel(ctx, m.ProviderID, m.ModelID)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE creative_models SET display_name=?,capability=?,protocol=?,price_json=?,constraints_json=?,source_group=?,enabled=?,updated_at=? WHERE id=?`, m.DisplayName, m.Capability, m.Protocol, m.PriceJSON, m.ConstraintsJSON, m.SourceGroup, boolInt(m.Enabled), now, m.ID)
	if err != nil {
		return nil, err
	}
	return s.GetCreativeModel(ctx, m.ID)
}
func (s *Store) GetCreativeModel(ctx context.Context, id int64) (*CreativeModel, error) {
	return scanCreativeModel(s.db.QueryRowContext(ctx, creativeModelSelect+` WHERE id=?`, id))
}
func (s *Store) GetCreativeModelByProviderModel(ctx context.Context, pid int64, model string) (*CreativeModel, error) {
	return scanCreativeModel(s.db.QueryRowContext(ctx, creativeModelSelect+` WHERE provider_id=? AND model_id=?`, pid, model))
}
func (s *Store) ListCreativeModels(ctx context.Context, pid int64, enabled bool) ([]CreativeModel, error) {
	q := creativeModelSelect + ` WHERE 1=1`
	args := []any{}
	if pid > 0 {
		q += ` AND provider_id=?`
		args = append(args, pid)
	}
	if enabled {
		q += ` AND enabled=1`
	}
	q += ` ORDER BY capability,display_name,model_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreativeModel{}
	for rows.Next() {
		m, err := scanCreativeModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
func scanCreativeModel(row interface{ Scan(...any) error }) (*CreativeModel, error) {
	var m CreativeModel
	var e int
	var c, u string
	if err := row.Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.DisplayName, &m.Capability, &m.Protocol, &m.PriceJSON, &m.ConstraintsJSON, &m.SourceGroup, &e, &c, &u); err != nil {
		return nil, err
	}
	m.Enabled = e != 0
	m.CreatedAt = parseStoreTime(c)
	m.UpdatedAt = parseStoreTime(u)
	return &m, nil
}

const creativeJobSelect = `SELECT id,order_no,request_key,user_id,provider_id,model_id,media_type,prompt,params_json,upstream_request_id,upstream_status,result_json,charge_amount,charge_status,charge_ledger_id,refund_ledger_id,status,progress,error_code,error_message,created_at,updated_at,completed_at,deleted_at FROM creative_jobs`

func (s *Store) CreateCreativeJob(ctx context.Context, j CreativeJob) (*CreativeJob, error) {
	now := time.Now().UTC()
	if j.Status == "" {
		j.Status = CreativeJobCreated
	}
	if j.ParamsJSON == "" {
		j.ParamsJSON = "{}"
	}
	if j.ResultJSON == "" {
		j.ResultJSON = "{}"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO creative_jobs(order_no,request_key,user_id,provider_id,model_id,media_type,prompt,params_json,upstream_request_id,upstream_status,result_json,charge_amount,charge_status,charge_ledger_id,refund_ledger_id,status,progress,error_code,error_message,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.OrderNo, j.RequestKey, j.UserID, j.ProviderID, j.ModelID, j.MediaType, j.Prompt, j.ParamsJSON, j.UpstreamRequestID, j.UpstreamStatus, j.ResultJSON, j.ChargeAmount, j.ChargeStatus, j.ChargeLedgerID, j.RefundLedgerID, j.Status, j.Progress, j.ErrorCode, j.ErrorMessage, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), "")
	if err != nil {
		return nil, err
	}
	j.ID, _ = res.LastInsertId()
	return s.GetCreativeJob(ctx, j.ID, 0)
}
func (s *Store) GetCreativeJobByRequestKey(ctx context.Context, uid int64, key string) (*CreativeJob, error) {
	return scanCreativeJob(s.db.QueryRowContext(ctx, creativeJobSelect+` WHERE user_id=? AND request_key=?`, uid, key))
}
func (s *Store) GetCreativeJob(ctx context.Context, id, uid int64) (*CreativeJob, error) {
	q := creativeJobSelect + ` WHERE id=?`
	args := []any{id}
	if uid > 0 {
		q += ` AND user_id=? AND deleted_at=''`
		args = append(args, uid)
	}
	return scanCreativeJob(s.db.QueryRowContext(ctx, q, args...))
}
func scanCreativeJob(row interface{ Scan(...any) error }) (*CreativeJob, error) {
	var j CreativeJob
	var c, u, d, deleted string
	if err := row.Scan(&j.ID, &j.OrderNo, &j.RequestKey, &j.UserID, &j.ProviderID, &j.ModelID, &j.MediaType, &j.Prompt, &j.ParamsJSON, &j.UpstreamRequestID, &j.UpstreamStatus, &j.ResultJSON, &j.ChargeAmount, &j.ChargeStatus, &j.ChargeLedgerID, &j.RefundLedgerID, &j.Status, &j.Progress, &j.ErrorCode, &j.ErrorMessage, &c, &u, &d, &deleted); err != nil {
		return nil, err
	}
	j.CreatedAt = parseStoreTime(c)
	j.UpdatedAt = parseStoreTime(u)
	if d != "" {
		t := parseStoreTime(d)
		j.CompletedAt = &t
	}
	if deleted != "" {
		t := parseStoreTime(deleted)
		j.DeletedAt = &t
	}
	return &j, nil
}
func (s *Store) UpdateCreativeJob(ctx context.Context, j CreativeJob) error {
	d := ""
	if j.CompletedAt != nil {
		d = j.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE creative_jobs SET upstream_request_id=?,upstream_status=?,result_json=?,charge_amount=?,charge_status=?,charge_ledger_id=?,refund_ledger_id=?,status=?,progress=?,error_code=?,error_message=?,updated_at=?,completed_at=? WHERE id=?`, j.UpstreamRequestID, j.UpstreamStatus, j.ResultJSON, j.ChargeAmount, j.ChargeStatus, j.ChargeLedgerID, j.RefundLedgerID, j.Status, j.Progress, j.ErrorCode, j.ErrorMessage, time.Now().UTC().Format(time.RFC3339Nano), d, j.ID)
	return err
}
func (s *Store) ListCreativeJobs(ctx context.Context, f CreativeJobFilter) ([]CreativeJob, int, error) {
	if f.Limit <= 0 {
		f.Limit = 10
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	q := ` WHERE 1=1`
	args := []any{}
	if f.UserID > 0 {
		q += ` AND user_id=?`
		args = append(args, f.UserID)
	}
	if f.MediaType != "" {
		q += ` AND media_type=?`
		args = append(args, f.MediaType)
	}
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, f.Status)
	}
	if !f.IncludeDeleted {
		q += ` AND deleted_at=''`
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM creative_jobs`+q, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, creativeJobSelect+q+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []CreativeJob{}
	for rows.Next() {
		j, err := scanCreativeJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *j)
	}
	return out, total, rows.Err()
}

func (s *Store) HideCreativeJob(ctx context.Context, id, userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `UPDATE creative_jobs SET deleted_at=?,updated_at=? WHERE id=? AND user_id=? AND deleted_at=''`, now, now, id, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) ListPendingCreativeVideos(ctx context.Context, limit int) ([]CreativeJob, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, creativeJobSelect+` WHERE media_type='video' AND status='processing' ORDER BY updated_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreativeJob{}
	for rows.Next() {
		j, err := scanCreativeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}
func (s *Store) ListPendingCreativeRefunds(ctx context.Context, limit int) ([]CreativeJob, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, creativeJobSelect+` WHERE status='failed' AND charge_status IN ('charged','refund_pending') AND refund_ledger_id=0 ORDER BY updated_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreativeJob{}
	for rows.Next() {
		j, err := scanCreativeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}
func (s *Store) AddCreativeJobEvent(ctx context.Context, e CreativeJobEvent) error {
	if e.DataJSON == "" {
		e.DataJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO creative_job_events(job_id,event_type,message,data_json,created_at) VALUES(?,?,?,?,?)`, e.JobID, e.EventType, e.Message, e.DataJSON, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func parseStoreTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	if t.IsZero() {
		t, _ = time.Parse(time.RFC3339, v)
	}
	return t
}
