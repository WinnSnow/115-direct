package pan115

import (
	"context"
	"time"
)

type Entry struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id"`
	Name      string    `json:"name"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	SHA1      string    `json:"sha1,omitempty"`
	PickCode  string    `json:"pick_code,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type ShareSnapshot struct {
	Code        string      `json:"code"`
	ReceiveCode string      `json:"-"`
	Title       string      `json:"title"`
	Entries     []Entry     `json:"entries"`
	Files       []ShareFile `json:"files,omitempty"`
}

type ShareFile struct {
	Relative string `json:"relative"`
	Entry    Entry  `json:"entry"`
}

type ShareTreeProvider interface {
	SnapshotShareTree(context.Context, string, string) (*ShareSnapshot, error)
}

type QRSession struct {
	ID      string `json:"id"`
	PNGData string `json:"png_data"`
}

type QRStatus struct {
	State  string `json:"state"`
	Cookie string `json:"-"`
}

type AccountInfo struct {
	UserID          int64     `json:"user_id"`
	Username        string    `json:"username"`
	VIP             bool      `json:"vip"`
	VIPLevel        int       `json:"vip_level"`
	VIPExpire       int64     `json:"vip_expire"`
	SpaceTotal      int64     `json:"space_total"`
	SpaceUsed       int64     `json:"space_used"`
	SpaceRemain     int64     `json:"space_remain"`
	SpaceTotalText  string    `json:"space_total_text,omitempty"`
	SpaceUsedText   string    `json:"space_used_text,omitempty"`
	SpaceRemainText string    `json:"space_remain_text,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AccountProvider interface {
	Account(ctx context.Context, refresh bool) (*AccountInfo, error)
}

type Provider interface {
	SetCookie(cookie string) error
	Check(ctx context.Context) error
	StartQR(ctx context.Context) (*QRSession, error)
	PollQR(ctx context.Context, id string) (*QRStatus, error)
	List(ctx context.Context, parentID string) ([]Entry, error)
	Mkdir(ctx context.Context, parentID, name string) (string, error)
	SnapshotShare(ctx context.Context, rawURL, receiveCode string) (*ShareSnapshot, error)
	ReceiveShare(ctx context.Context, snapshot *ShareSnapshot, receiveCode, targetCID string) error
	Copy(ctx context.Context, targetCID string, ids ...string) error
	Move(ctx context.Context, targetCID string, ids ...string) error
	Rename(ctx context.Context, id, name string) error
	Delete(ctx context.Context, ids ...string) error
	DownloadURL(ctx context.Context, pickCode, userAgent string) (string, map[string]string, error)
}
