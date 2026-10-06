package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func normalizeUserRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "operator", "viewer":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "operator"
	}
}

func validateUsername(username string) error {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 || strings.ContainsAny(username, "\r\n\t /\\") {
		return fmt.Errorf("用户名无效")
	}
	return nil
}

func (s *Store) EnsureUser(ctx context.Context, username, passwordHash, role string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	if strings.TrimSpace(passwordHash) == "" {
		return fmt.Errorf("密码哈希不能为空")
	}
	role = normalizeUserRole(role)
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(username) DO NOTHING`, username, passwordHash, role, true, now, now)
	return err
}

func (s *Store) CreateUser(ctx context.Context, user User) error {
	if err := validateUsername(user.Username); err != nil {
		return err
	}
	if strings.TrimSpace(user.PasswordHash) == "" {
		return fmt.Errorf("密码哈希不能为空")
	}
	user.Role = normalizeUserRole(user.Role)
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		user.Username, user.PasswordHash, user.Role, user.Enabled, now, now)
	return err
}

func (s *Store) GetUser(ctx context.Context, username string) (*User, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `SELECT username,password_hash,role,enabled,created_at,updated_at FROM users WHERE username=?`, username).
		Scan(&user.Username, &user.PasswordHash, &user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	user.Role = normalizeUserRole(user.Role)
	return &user, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username,password_hash,role,enabled,created_at,updated_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.Username, &user.PasswordHash, &user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		user.Role = normalizeUserRole(user.Role)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, username string, role string, enabled bool) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE users SET role=?,enabled=?,updated_at=? WHERE username=?`, normalizeUserRole(role), enabled, time.Now().UTC(), username)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateUserPassword(ctx context.Context, username, passwordHash string) error {
	if strings.TrimSpace(passwordHash) == "" {
		return fmt.Errorf("密码哈希不能为空")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=?,updated_at=? WHERE username=?`, passwordHash, time.Now().UTC(), username)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE username=?`, username)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
