package pan115

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SheltonZhu/115driver/pkg/driver"
)

const unifiedUA = driver.UADefault

const (
	androidFileListEndpoint = "https://proapi.115.com/android/2.0/ufile/files"
	shareSnapshotEndpoint   = "https://115cdn.com/webapi/share/snap"
	shareReceiveEndpoint    = "https://webapi.115.com/share/receive"
)

var (
	shareCodePattern     = regexp.MustCompile(`(?i)(?:115\.com|115cdn\.com|anxia\.com)/s/([A-Za-z0-9_-]+)`)
	sharePasswordPattern = regexp.MustCompile(`(?i)[?&](?:password|receive_code|pwd)=([^&#\s]+)`)
)

type DriverProvider struct {
	mu            sync.RWMutex
	client        *driver.Pan115Client
	cookie        string
	http          *http.Client
	qrMu          sync.Mutex
	qr            map[string]*driver.QRCodeSession
	qrDone        map[string]terminalQRStatus
	accountMu     sync.Mutex
	account       *AccountInfo
	accountAt     time.Time
	mutationMu    sync.Mutex
	lastMutation  time.Time
	shareReadMu   sync.Mutex
	lastShareRead time.Time
}

type terminalQRStatus struct {
	status  QRStatus
	expires time.Time
}

func New() *DriverProvider {
	return &DriverProvider{
		client: driver.Default(),
		http:   &http.Client{Timeout: 30 * time.Second},
		qr:     map[string]*driver.QRCodeSession{},
		qrDone: map[string]terminalQRStatus{},
	}
}

func (p *DriverProvider) ConfigureHTTP(client *http.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.http = client
	c := driver.New(driver.WithClient(client))
	if p.cookie != "" {
		var credential driver.Credential
		if credential.FromCookie(p.cookie) == nil {
			c.ImportCredential(&credential)
		}
	}
	p.client = c
}
func (p *DriverProvider) newClient() *driver.Pan115Client {
	return driver.New(driver.WithClient(p.http))
}

func (p *DriverProvider) SetCookie(cookie string) error {
	var credential driver.Credential
	if err := credential.FromCookie(cookie); err != nil {
		return err
	}
	p.mu.Lock()
	p.client = p.newClient().ImportCredential(&credential)
	p.cookie = credential.Cookie()
	p.mu.Unlock()
	p.accountMu.Lock()
	p.account = nil
	p.accountAt = time.Time{}
	p.accountMu.Unlock()
	return nil
}

func (p *DriverProvider) current() (*driver.Pan115Client, string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cookie == "" {
		return nil, "", fmt.Errorf("115 cookie is not configured")
	}
	return p.client, p.cookie, nil
}

func (p *DriverProvider) Check(ctx context.Context) error {
	c, _, err := p.current()
	if err != nil {
		return err
	}
	return runContext(ctx, c.CookieCheck)
}

func (p *DriverProvider) StartQR(ctx context.Context) (*QRSession, error) {
	c := p.newClient()
	session, err := c.QRCodeStart()
	if err != nil {
		return nil, err
	}
	png, err := session.QRCode()
	if err != nil {
		return nil, err
	}
	idBytes := make([]byte, 18)
	if _, err = rand.Read(idBytes); err != nil {
		return nil, err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	p.qrMu.Lock()
	p.pruneQRLocked(time.Now())
	p.qr[id] = session
	p.qrMu.Unlock()
	return &QRSession{ID: id, PNGData: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, nil
}

func (p *DriverProvider) PollQR(ctx context.Context, id string) (*QRStatus, error) {
	p.qrMu.Lock()
	defer p.qrMu.Unlock()
	p.pruneQRLocked(time.Now())
	if done, ok := p.qrDone[id]; ok {
		status := done.status
		return &status, nil
	}
	session := p.qr[id]
	if session == nil {
		return nil, fmt.Errorf("QR session not found")
	}
	c := p.newClient()
	status, err := c.QRCodeStatus(session)
	if err != nil {
		return nil, err
	}
	switch {
	case status.IsWaiting():
		return &QRStatus{State: "waiting"}, nil
	case status.IsScanned():
		return &QRStatus{State: "scanned"}, nil
	case status.IsExpired():
		return p.finishQRLocked(id, QRStatus{State: "expired"}), nil
	case status.IsCanceled():
		return p.finishQRLocked(id, QRStatus{State: "canceled"}), nil
	case status.IsAllowed():
		credential, err := c.QRCodeLoginWithApp(session, driver.LoginQAppAndroid)
		if err != nil {
			return nil, err
		}
		cookie := credential.Cookie()
		if _, err = p.listAndroid(ctx, cookie, "0"); err != nil {
			return nil, fmt.Errorf("qandroid 目录校验失败: %w", err)
		}
		if err = p.SetCookie(cookie); err != nil {
			return nil, err
		}
		return p.finishQRLocked(id, QRStatus{State: "confirmed", Cookie: cookie}), nil
	default:
		return &QRStatus{State: "unknown"}, nil
	}
}

func (p *DriverProvider) finishQRLocked(id string, status QRStatus) *QRStatus {
	delete(p.qr, id)
	p.qrDone[id] = terminalQRStatus{status: status, expires: time.Now().Add(5 * time.Minute)}
	result := status
	return &result
}

func (p *DriverProvider) pruneQRLocked(now time.Time) {
	for id, done := range p.qrDone {
		if !now.Before(done.expires) {
			delete(p.qrDone, id)
		}
	}
}

func (p *DriverProvider) List(ctx context.Context, parentID string) ([]Entry, error) {
	c, cookie, err := p.current()
	if err != nil {
		return nil, err
	}
	entries, androidErr := p.listAndroid(ctx, cookie, parentID)
	if androidErr == nil {
		return entries, nil
	}
	var files *[]driver.File
	var webErr error
	for _, endpoint := range []string{driver.ApiFileList, driver.ApiFileListByName} {
		files, err = callContext(ctx, func() (*[]driver.File, error) {
			return c.List(parentID, driver.WithApiURLs(endpoint))
		})
		if err == nil {
			break
		}
		if webErr == nil {
			webErr = err
		}
	}
	if err != nil {
		return nil, fmt.Errorf("list 115 directory %s: android: %v; web: %v; aps: %w", parentID, androidErr, webErr, err)
	}
	entries = make([]Entry, 0, len(*files))
	for _, f := range *files {
		entries = append(entries, Entry{ID: f.FileID, ParentID: f.ParentID, Name: f.Name, Directory: f.IsDirectory,
			Size: f.Size, SHA1: f.Sha1, PickCode: f.PickCode, CreatedAt: f.CreateTime, UpdatedAt: f.UpdateTime})
	}
	return entries, nil
}

func (p *DriverProvider) listAndroid(ctx context.Context, cookie, parentID string) ([]Entry, error) {
	const limit = 1150
	entries := make([]Entry, 0)
	for offset := 0; ; offset += limit {
		query := url.Values{
			"aid": {"1"}, "cid": {parentID}, "limit": {fmt.Sprint(limit)}, "offset": {fmt.Sprint(offset)},
			"show_dir": {"1"}, "count_folders": {"1"}, "record_open_time": {"1"},
		}
		var response struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Count int    `json:"count"`
			Data  []struct {
				ID        string             `json:"fid"`
				ParentID  string             `json:"pid"`
				Name      string             `json:"fn"`
				FileClass driver.StringInt   `json:"fc"`
				Size      driver.StringInt64 `json:"fs"`
				SHA1      string             `json:"sha1"`
				PickCode  string             `json:"pc"`
				CreatedAt driver.StringInt64 `json:"uppt"`
				UpdatedAt driver.StringInt64 `json:"uet"`
			} `json:"data"`
		}
		if err := p.shareRequest(ctx, "android file list", http.MethodGet, androidFileListEndpoint, query, nil,
			cookie, "https://115.com/", &response); err != nil {
			return nil, err
		}
		if !response.State {
			return nil, fmt.Errorf("android file list: %s", response.Error)
		}
		for _, item := range response.Data {
			entries = append(entries, Entry{
				ID: item.ID, ParentID: item.ParentID, Name: item.Name, Directory: item.FileClass == 0,
				Size: int64(item.Size), SHA1: item.SHA1, PickCode: item.PickCode,
				CreatedAt: time.Unix(int64(item.CreatedAt), 0), UpdatedAt: time.Unix(int64(item.UpdatedAt), 0),
			})
		}
		if offset+len(response.Data) >= response.Count || len(response.Data) == 0 {
			return entries, nil
		}
	}
}

func (p *DriverProvider) Mkdir(ctx context.Context, parentID, name string) (string, error) {
	return runMutationCall(p, ctx, func(c *driver.Pan115Client) (string, error) { return c.Mkdir(parentID, name) })
}

func (p *DriverProvider) Copy(ctx context.Context, targetCID string, ids ...string) error {
	_, err := runMutationCall(p, ctx, func(c *driver.Pan115Client) (struct{}, error) {
		return struct{}{}, c.Copy(targetCID, ids...)
	})
	return err
}

func (p *DriverProvider) Move(ctx context.Context, targetCID string, ids ...string) error {
	_, err := runMutationCall(p, ctx, func(c *driver.Pan115Client) (struct{}, error) {
		return struct{}{}, c.Move(targetCID, ids...)
	})
	return err
}

func (p *DriverProvider) Rename(ctx context.Context, id, name string) error {
	_, err := runMutationCall(p, ctx, func(c *driver.Pan115Client) (struct{}, error) {
		return struct{}{}, c.Rename(id, name)
	})
	return err
}

func (p *DriverProvider) Delete(ctx context.Context, ids ...string) error {
	_, err := runMutationCall(p, ctx, func(c *driver.Pan115Client) (struct{}, error) {
		return struct{}{}, c.Delete(ids...)
	})
	return err
}

// runMutationCall keeps the mutation gate held until the SDK request actually
// returns. When the caller context expires first, the handler returns promptly
// while the in-flight operation finishes before another mutation is admitted.
func runMutationCall[T any](p *DriverProvider, ctx context.Context, fn func(*driver.Pan115Client) (T, error)) (T, error) {
	var zero T
	if err := p.beginMutation(ctx); err != nil {
		return zero, err
	}
	c, _, err := p.current()
	if err != nil {
		p.mutationMu.Unlock()
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, callErr := fn(c)
		done <- result{value: value, err: callErr}
	}()
	select {
	case result := <-done:
		p.mutationMu.Unlock()
		return result.value, result.err
	case <-ctx.Done():
		go func() {
			<-done
			p.mutationMu.Unlock()
		}()
		return zero, ctx.Err()
	}
}

func (p *DriverProvider) beginMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mutationMu.Lock()
	delay := time.Until(p.lastMutation.Add(time.Second))
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			p.mutationMu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.lastMutation = time.Now()
	if err := ctx.Err(); err != nil {
		p.mutationMu.Unlock()
		return err
	}
	return nil
}

func (p *DriverProvider) DownloadURL(ctx context.Context, pickCode, userAgent string) (string, map[string]string, error) {
	c, _, err := p.current()
	if err != nil {
		return "", nil, err
	}
	info, err := callContext(ctx, func() (*driver.DownloadInfo, error) { return c.DownloadWithUA(pickCode, userAgent) })
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{}
	for key, values := range info.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return info.Url.Url, headers, nil
}

func (p *DriverProvider) SnapshotShare(ctx context.Context, rawURL, receiveCode string) (*ShareSnapshot, error) {
	code, receiveCode, err := parseShareLink(rawURL, receiveCode)
	if err != nil {
		return nil, err
	}
	return p.snapshotShareDirectory(ctx, code, receiveCode, "0")
}

func (p *DriverProvider) snapshotShareDirectory(ctx context.Context, code, receiveCode, parent string) (*ShareSnapshot, error) {
	_, cookie, err := p.current()
	if err != nil {
		return nil, err
	}
	result := &ShareSnapshot{Code: code, ReceiveCode: receiveCode}
	for offset := 0; ; offset += 1150 {
		query := url.Values{"share_code": {code}, "receive_code": {receiveCode}, "cid": {parent},
			"offset": {fmt.Sprint(offset)}, "limit": {"1150"}, "asc": {"1"}, "fc_mix": {"0"}, "format": {"json"}}
		var response struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Data  struct {
				List []struct {
					FID      shareID            `json:"fid"`
					CID      shareID            `json:"cid"`
					Name     string             `json:"n"`
					FileName string             `json:"file_name"`
					SHA      string             `json:"sha"`
					SHA1     string             `json:"sha1"`
					PickCode string             `json:"pc"`
					Size     driver.StringInt64 `json:"s"`
					FileType int                `json:"fc"`
				} `json:"list"`
				ShareInfo struct {
					Title string `json:"share_title"`
				} `json:"shareinfo"`
			} `json:"data"`
		}
		if err := p.waitShareRead(ctx); err != nil {
			return nil, err
		}
		if err := p.shareRequest(ctx, "share snapshot", http.MethodGet, shareSnapshotEndpoint, query, nil, cookie,
			shareReferer(code, receiveCode), &response); err != nil {
			return nil, err
		}
		if !response.State {
			return nil, fmt.Errorf("share snapshot: %s", response.Error)
		}
		if result.Title == "" {
			result.Title = response.Data.ShareInfo.Title
		}
		for _, item := range response.Data.List {
			name := item.Name
			if name == "" {
				name = item.FileName
			}
			id, directory := string(item.FID), item.FileType == 0
			if id == "" {
				id, directory = string(item.CID), true
			}
			sha := item.SHA
			if sha == "" {
				sha = item.SHA1
			}
			result.Entries = append(result.Entries, Entry{ID: id, ParentID: parent, Name: name, Directory: directory, Size: int64(item.Size), SHA1: sha, PickCode: item.PickCode})
		}
		if len(response.Data.List) < 1150 {
			break
		}
	}
	return result, nil
}

func (p *DriverProvider) waitShareRead(ctx context.Context) error {
	p.shareReadMu.Lock()
	defer p.shareReadMu.Unlock()
	if delay := time.Second - time.Since(p.lastShareRead); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.lastShareRead = time.Now()
	return ctx.Err()
}

// Inspect nested share folders: root names alone cannot reveal newly added episodes.
func (p *DriverProvider) SnapshotShareTree(ctx context.Context, rawURL, receiveCode string) (*ShareSnapshot, error) {
	root, err := p.SnapshotShare(ctx, rawURL, receiveCode)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{"0": true}
	paths := map[string]bool{}
	var walk func([]Entry, string, int) error
	walk = func(entries []Entry, prefix string, depth int) error {
		if depth > 64 || len(seen) > 2000 {
			return fmt.Errorf("分享目录层级或数量超过检查上限")
		}
		for _, e := range entries {
			if e.ID == "" || e.Name == "" || e.Name == "." || e.Name == ".." || strings.ContainsAny(e.Name, "/\\\x00\r\n") {
				return fmt.Errorf("分享文件路径无效")
			}
			rel := path.Join(prefix, e.Name)
			if paths[rel] {
				return fmt.Errorf("分享中存在重名路径，请先核对：%s", rel)
			}
			paths[rel] = true
			if e.Directory {
				if seen[e.ID] {
					return fmt.Errorf("分享目录循环或重复引用")
				}
				seen[e.ID] = true
				child, err := p.snapshotShareDirectory(ctx, root.Code, root.ReceiveCode, e.ID)
				if err != nil {
					return err
				}
				if err := walk(child.Entries, rel, depth+1); err != nil {
					return err
				}
			} else {
				root.Files = append(root.Files, ShareFile{Relative: rel, Entry: e})
				if len(root.Files) > 100000 {
					return fmt.Errorf("分享文件数量超过检查上限")
				}
			}
		}
		return nil
	}
	if err := walk(root.Entries, "", 0); err != nil {
		return nil, err
	}
	return root, nil
}

func (p *DriverProvider) ReceiveShare(ctx context.Context, snapshot *ShareSnapshot, receiveCode, targetCID string) error {
	if err := p.beginMutation(ctx); err != nil {
		return err
	}
	defer p.mutationMu.Unlock()
	_, cookie, err := p.current()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		ids = append(ids, entry.ID)
	}
	if strings.TrimSpace(receiveCode) == "" {
		receiveCode = snapshot.ReceiveCode
	}
	form := url.Values{"share_code": {snapshot.Code}, "receive_code": {receiveCode}, "file_id": {strings.Join(ids, ",")}, "cid": {targetCID}}
	var response struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if err := p.shareRequest(ctx, "share receive", http.MethodPost, shareReceiveEndpoint, nil, form, cookie,
		shareReferer(snapshot.Code, receiveCode), &response); err != nil {
		return err
	}
	if !response.State {
		return fmt.Errorf("share receive: %s", response.Error)
	}
	return nil
}

func parseShareLink(rawURL, explicitCode string) (string, string, error) {
	match := shareCodePattern.FindStringSubmatch(rawURL)
	if len(match) != 2 {
		return "", "", fmt.Errorf("invalid 115 share URL")
	}
	receiveCode := strings.TrimSpace(explicitCode)
	if receiveCode == "" {
		if parsed, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
			for _, key := range []string{"password", "receive_code", "pwd"} {
				if value := strings.TrimSpace(parsed.Query().Get(key)); value != "" {
					receiveCode = value
					break
				}
			}
		}
	}
	if receiveCode == "" {
		if password := sharePasswordPattern.FindStringSubmatch(rawURL); len(password) == 2 {
			receiveCode, _ = url.QueryUnescape(password[1])
			receiveCode = strings.TrimSpace(receiveCode)
		}
	}
	return match[1], receiveCode, nil
}

func shareReferer(code, receiveCode string) string {
	return "https://115cdn.com/s/" + code + "?password=" + url.QueryEscape(receiveCode) + "&"
}

func (p *DriverProvider) shareRequest(ctx context.Context, operation, method, endpoint string, query, form url.Values,
	cookie, referer string, out any,
) error {
	if query != nil {
		endpoint += "?" + query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", unifiedUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if readErr != nil {
			return fmt.Errorf("115 %s HTTP %d (read response: %v)", operation, resp.StatusCode, readErr)
		}
		detail := strings.Join(strings.Fields(string(body)), " ")
		if detail == "" {
			return fmt.Errorf("115 %s HTTP %d", operation, resp.StatusCode)
		}
		return fmt.Errorf("115 %s HTTP %d: %s", operation, resp.StatusCode, detail)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("115 %s response: %w", operation, err)
	}
	return nil
}

func runContext(ctx context.Context, fn func() error) error {
	_, err := callContext(ctx, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

func callContext[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	ch := make(chan result, 1)
	go func() { value, err := fn(); ch <- result{value, err} }()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case result := <-ch:
		return result.value, result.err
	}
}
