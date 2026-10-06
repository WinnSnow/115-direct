package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var secretLogRE = regexp.MustCompile(`(?i)(cookie|authorization|callback_access_token|access_token|access_key|token|secret|password|api[_-]?key|sig|jellyfin_sig|sign|receive_code|share_code)\s*[:=]\s*(?:Bearer\s+)?[^\r\n&,;]+`)
var queryLogRE = regexp.MustCompile(`https?://[^\s?]+\?[^\s]+`)
var credentialLogRE = regexp.MustCompile(`(?i)(https?|socks5h?)://[^/@\s]+:[^/@\s]+@`)

func Redact(message string) string {
	message = credentialLogRE.ReplaceAllString(message, "$1://[redacted]@")
	message = queryLogRE.ReplaceAllString(message, "[signed-url]")
	message = secretLogRE.ReplaceAllString(message, "$1=[redacted]")
	if len(message) > 8192 {
		message = message[:8192]
	}
	return message
}

type LogFilter struct {
	Category string
	Level    string
	Search   string
	Limit    int
	Offset   int
}

func (s *Store) QueryLogs(ctx context.Context, f LogFilter) ([]AuditEvent, int, error) {
	if f.Limit < 1 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := " WHERE 1=1"
	args := []any{}
	if f.Category != "" {
		where += " AND category=?"
		args = append(args, f.Category)
	}
	if f.Level != "" {
		where += " AND level=?"
		args = append(args, f.Level)
	}
	if f.Search != "" {
		where += " AND message LIKE ?"
		args = append(args, "%"+f.Search+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,category,level,message,created_at FROM audit_events`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.Category, &e.Level, &e.Message, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		e.Message = Redact(e.Message)
		items = append(items, e)
	}
	return items, total, rows.Err()
}

type LogConfig struct {
	Days       int    `json:"days"`
	MaxEntries int    `json:"max_entries"`
	Level      string `json:"level"`
	MaxBytes   int    `json:"max_bytes"`
}

func DefaultLogConfig() LogConfig { return LogConfig{30, 50000, "info", 50 << 20} }
func (c LogConfig) Validate() error {
	if c.MaxBytes != 0 && (c.MaxBytes < 1<<20 || c.MaxBytes > 1<<30) {
		return fmt.Errorf("日志字节上限须为1MB至1GB")
	}
	if c.Days < 1 || c.Days > 365 || c.MaxEntries < 100 || c.MaxEntries > 500000 {
		return fmt.Errorf("日志保留天数须为1至365，总条数须为100至500000")
	}
	if !strings.Contains("|debug|info|warning|error|", "|"+c.Level+"|") {
		return fmt.Errorf("日志级别无效")
	}
	return nil
}
func (s *Store) Log(ctx context.Context, category, level, message string) {
	c := DefaultLogConfig()
	_ = s.GetSetting(ctx, "logging", &c)
	if c.Validate() != nil {
		c = DefaultLogConfig()
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 50 << 20
	}
	levels := map[string]int{"debug": 0, "info": 1, "warning": 2, "error": 3}
	if category == "runtime" && levels[level] < levels[c.Level] {
		return
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_events(category,level,message,created_at) VALUES(?,?,?,?)`, category, level, Redact(message), time.Now().UTC())
	_, _ = s.db.ExecContext(ctx, `DELETE FROM audit_events WHERE created_at<? OR id NOT IN (SELECT id FROM audit_events ORDER BY id DESC LIMIT ?)`, time.Now().UTC().Add(-time.Duration(c.Days)*24*time.Hour), c.MaxEntries)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM audit_events WHERE id IN (SELECT id FROM (SELECT id,SUM(length(CAST(message AS BLOB))+128) OVER (ORDER BY id DESC) AS bytes FROM audit_events) WHERE bytes>?)`, c.MaxBytes)
}
func (s *Store) migrateLogs() error {
	rows, err := s.db.Query(`PRAGMA table_info(audit_events)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "level" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		_, err = s.db.Exec(`ALTER TABLE audit_events ADD COLUMN level TEXT NOT NULL DEFAULT 'info'`)
	}
	return err
}
