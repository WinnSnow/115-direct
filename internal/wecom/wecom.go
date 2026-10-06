package wecom

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/syncer"
)

type Config struct {
	Enabled             bool     `json:"enabled"`
	CorpID              string   `json:"corp_id"`
	AgentID             string   `json:"agent_id"`
	Secret              string   `json:"secret"`
	Token               string   `json:"token"`
	EncodingAESKey      string   `json:"encoding_aes_key"`
	CallbackAccessToken string   `json:"callback_access_token"`
	CallbackBaseURL     string   `json:"callback_base_url"`
	ShareEnabled        *bool    `json:"share_enabled,omitempty"`
	ShareRepeatPolicy   string   `json:"share_repeat_policy,omitempty"`
	ShareCheckMinutes   *int     `json:"share_check_minutes,omitempty"`
	MenuEnabled         bool     `json:"menu_enabled,omitempty"`
	MenuOrganize        bool     `json:"menu_organize,omitempty"`
	MenuFullSync        bool     `json:"menu_full_sync,omitempty"`
	MenuIncrementalSync bool     `json:"menu_incremental_sync,omitempty"`
	AllowUsers          []string `json:"allow_users"`
}

type Handler struct {
	Store       *store.Store
	Jobs        *organize.Service
	Sync        *syncer.Service
	Config      func(context.Context) (Config, error)
	HTTP        *http.Client
	mu          sync.Mutex
	accessToken string
	tokenUntil  time.Time
}

var (
	tmdbRE = regexp.MustCompile(`(?i)tmdb\s*[:：=]\s*(movie|tv)\s*[:：=]\s*(\d+)`)
)

func NewHandler(st *store.Store, jobs *organize.Service, loader func(context.Context) (Config, error)) *Handler {
	return &Handler{Store: st, Jobs: jobs, Config: loader, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.Config(r.Context())
	if err != nil || !cfg.Enabled {
		http.Error(w, "callback not configured", http.StatusServiceUnavailable)
		return
	}
	if cfg.CallbackAccessToken == "" {
		http.Error(w, "callback access token not configured", http.StatusServiceUnavailable)
		return
	}
	// Gate all methods before reading or decoding the callback body.
	values := r.URL.Query()["access_token"]
	var supplied string
	if len(values) == 1 {
		supplied = values[0]
	}
	expectedHash, suppliedHash := sha256.Sum256([]byte(cfg.CallbackAccessToken)), sha256.Sum256([]byte(supplied))
	if supplied == "" || subtle.ConstantTimeCompare(expectedHash[:], suppliedHash[:]) != 1 {
		http.Error(w, "callback access denied", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		h.verify(w, r, cfg)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var envelope struct {
		Encrypt string `xml:"Encrypt"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "invalid callback body", http.StatusRequestEntityTooLarge)
		return
	}
	if xml.Unmarshal(body, &envelope) != nil || envelope.Encrypt == "" {
		http.Error(w, "invalid callback payload", http.StatusBadRequest)
		return
	}
	if Signature(cfg.Token, r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"), envelope.Encrypt) != r.URL.Query().Get("msg_signature") {
		http.Error(w, "signature mismatch", http.StatusForbidden)
		return
	}
	plain, err := Decrypt(cfg.EncodingAESKey, cfg.CorpID, envelope.Encrypt)
	if err != nil {
		http.Error(w, "decrypt failed", http.StatusBadRequest)
		return
	}
	var message struct {
		FromUser   string `xml:"FromUserName"`
		MsgType    string `xml:"MsgType"`
		Content    string `xml:"Content"`
		MsgID      string `xml:"MsgId"`
		CreateTime int64  `xml:"CreateTime"`
		Event      string `xml:"Event"`
		EventKey   string `xml:"EventKey"`
	}
	if xml.Unmarshal(plain, &message) == nil && (message.MsgType == "text" || message.MsgType == "event") {
		if !allowed(cfg.AllowUsers, message.FromUser) {
			h.audit(r.Context(), fmt.Sprintf("企业微信消息被 UserID 白名单拒绝：%s", message.FromUser))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
			return
		}
		dedup := message.MsgID
		if dedup == "" {
			sum := sha256.Sum256([]byte(message.FromUser + strconv.FormatInt(message.CreateTime, 10) + message.MsgType + message.Event + message.EventKey + message.Content))
			dedup = hex.EncodeToString(sum[:])
		}
		fresh := true
		if h.Store != nil {
			fresh, _ = h.Store.DeduplicateMessage(r.Context(), dedup)
		}
		if fresh {
			h.audit(r.Context(), fmt.Sprintf("企业微信消息已接收：类型=%s 事件=%s 按钮=%s", message.MsgType, message.Event, message.EventKey))
			if message.MsgType == "text" {
				go h.handleText(message.FromUser, message.Content)
			} else {
				go h.handleEvent(message.FromUser, message.Event, message.EventKey)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}

func (h *Handler) audit(ctx context.Context, message string) {
	if h.Store != nil {
		h.Store.Audit(ctx, "wecom", message)
	}
}

func (h *Handler) handleEvent(sender, event, key string) {
	if !strings.EqualFold(event, "click") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg, err := h.Config(ctx)
	if err != nil {
		return
	}
	if !cfg.MenuEnabled {
		_ = h.Send(ctx, sender, "应用菜单操作未启用，请在系统设置中开启并同步菜单。")
		return
	}
	switch key {
	case "115_organize":
		if !cfg.MenuOrganize {
			_ = h.Send(ctx, sender, "网盘整理按钮已关闭。")
			return
		}
		if h.Jobs == nil {
			_ = h.Send(ctx, sender, "整理服务尚未就绪。")
			return
		}
		result, err := h.Jobs.ReconcileLibrary(ctx)
		if err != nil {
			_ = h.Send(ctx, sender, "网盘整理启动失败："+err.Error())
			return
		}
		_ = h.Send(ctx, sender, fmt.Sprintf("网盘整理已执行：新建目录 %d，移动 %d，跳过 %d。", result.Created, result.Moved, result.Skipped))
	case "115_full_sync", "115_incremental_sync":
		full := key == "115_full_sync"
		if full && !cfg.MenuFullSync || !full && !cfg.MenuIncrementalSync {
			_ = h.Send(ctx, sender, "该同步按钮已关闭。")
			return
		}
		if h.Sync == nil {
			_ = h.Send(ctx, sender, "同步服务尚未就绪。")
			return
		}
		if err := h.Sync.Queue(full); err != nil {
			_ = h.Send(ctx, sender, "同步未启动："+err.Error())
			return
		}
		if full {
			_ = h.Send(ctx, sender, "全量同步已排队。正在运行时再次点击会提示任务进行中。")
		} else {
			_ = h.Send(ctx, sender, "增量同步已排队。正在运行时再次点击会提示任务进行中。")
		}
	}
}

type menuButton struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// SyncMenu applies the enabled buttons to the WeCom application menu. It is
// explicit so changing a switch never makes an external API call implicitly.
func (h *Handler) SyncMenu(ctx context.Context) error {
	cfg, err := h.Config(ctx)
	if err != nil {
		return err
	}
	token, err := h.token(ctx, cfg)
	if err != nil {
		return err
	}
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/menu/"
	menuQuery := "?access_token=" + url.QueryEscape(token) + "&agentid=" + url.QueryEscape(cfg.AgentID)
	if !cfg.MenuEnabled {
		endpoint += "delete" + menuQuery
	} else {
		buttons := make([]menuButton, 0, 3)
		if cfg.MenuOrganize {
			buttons = append(buttons, menuButton{Type: "click", Name: "网盘整理", Key: "115_organize"})
		}
		if cfg.MenuFullSync {
			buttons = append(buttons, menuButton{Type: "click", Name: "全量同步", Key: "115_full_sync"})
		}
		if cfg.MenuIncrementalSync {
			buttons = append(buttons, menuButton{Type: "click", Name: "增量同步", Key: "115_incremental_sync"})
		}
		if len(buttons) == 0 {
			return fmt.Errorf("至少启用一个企业微信应用按钮")
		}
		endpoint += "create" + menuQuery
		body, _ := json.Marshal(map[string]any{"button": buttons})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		return h.menuResponse(req)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	return h.menuResponse(req)
}

func (h *Handler) menuResponse(req *http.Request) error {
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("WeCom menu: %d %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request, cfg Config) {
	encrypted := r.URL.Query().Get("echostr")
	if Signature(cfg.Token, r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"), encrypted) != r.URL.Query().Get("msg_signature") {
		http.Error(w, "signature mismatch", http.StatusForbidden)
		return
	}
	plain, err := Decrypt(cfg.EncodingAESKey, cfg.CorpID, encrypted)
	if err != nil {
		http.Error(w, "decrypt failed", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(plain)
}

func (h *Handler) handleText(sender, content string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg, err := h.Config(ctx)
	if err != nil {
		return
	}
	if cfg.ShareEnabled != nil && !*cfg.ShareEnabled {
		_ = h.Send(ctx, sender, "分享链接自动识别与转存已关闭。")
		return
	}
	message, err := pan115.ParseShareMessage(content)
	if err != nil {
		_ = h.Send(ctx, sender, err.Error())
		return
	}
	kind := ""
	var tmdbID int64
	if match := tmdbRE.FindStringSubmatch(content); len(match) == 3 {
		kind = strings.ToLower(match[1])
		tmdbID, _ = strconv.ParseInt(match[2], 10, 64)
	}
	minutes := 10
	if cfg.ShareCheckMinutes != nil {
		minutes = *cfg.ShareCheckMinutes
	}
	force := strings.HasPrefix(strings.TrimSpace(content), "检查更新") || strings.HasPrefix(strings.TrimSpace(content), "檢查更新")
	job, err := h.Jobs.SubmitShareMessage(ctx, sender, message.URL, message.Code, kind, tmdbID, organize.ShareSubmissionOptions{CheckUpdates: cfg.ShareRepeatPolicy != "skip", CheckInterval: time.Duration(minutes) * time.Minute, Force: force})
	if err != nil {
		_ = h.Send(context.Background(), sender, "任务创建失败："+err.Error())
		return
	}
	_ = h.Send(ctx, sender, job.SubmissionMessage)
}

func (h *Handler) Send(ctx context.Context, user, content string) error {
	cfg, err := h.Config(ctx)
	if err != nil || !cfg.Enabled {
		return err
	}
	token, err := h.token(ctx, cfg)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"touser": user, "msgtype": "text", "agentid": cfg.AgentID, "text": map[string]string{"content": content}, "safe": 0})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token="+url.QueryEscape(token), strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil || result.ErrCode != 0 {
		return fmt.Errorf("WeCom send: %d %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}

func (h *Handler) token(ctx context.Context, cfg Config) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.accessToken != "" && time.Now().Before(h.tokenUntil) {
		return h.accessToken, nil
	}
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=" + url.QueryEscape(cfg.CorpID) + "&corpsecret=" + url.QueryEscape(cfg.Secret)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		Expires     int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || result.ErrCode != 0 {
		return "", fmt.Errorf("WeCom token: %d %s", result.ErrCode, result.ErrMsg)
	}
	h.accessToken = result.AccessToken
	h.tokenUntil = time.Now().Add(time.Duration(result.Expires-120) * time.Second)
	return h.accessToken, nil
}

func allowed(list []string, user string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if item == user {
			return true
		}
	}
	return false
}
