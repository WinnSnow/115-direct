package pan115

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

const openUploadInitURL = "https://proapi.115.com/open/upload/init"

// OpenCredentials are kept separate from the web Cookie.  A refresh token is
// intentionally accepted here but never logged or returned by an API handler.
type OpenCredentials struct {
	ClientID     string `json:"client_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

var ErrOpenUploadRequiresDataChannel = errors.New("Open API 未返回秒传，需使用 OSS 数据通道")

type OpenUploader struct {
	HTTP    *http.Client
	Creds   OpenCredentials
	List    func(context.Context, string) ([]Entry, error)
	BaseURL string
}

func (u *OpenUploader) endpoint(path, fallback string) string {
	if u.BaseURL != "" {
		return strings.TrimRight(u.BaseURL, "/") + path
	}
	return fallback
}

// Upload performs the official rapid-upload initialization.  The Open API
// response's status=2 is a complete server-side upload.  Non-rapid uploads
// are reported explicitly so the task runner can use the Cookie OSS channel
// without accidentally sending the same file twice.
func (u *OpenUploader) Upload(ctx context.Context, localPath, parentID, filename string) (*UploadResult, error) {
	if strings.TrimSpace(u.Creds.AccessToken) == "" {
		return nil, fmt.Errorf("Open OAuth 未授权")
	}
	if u.HTTP == nil {
		u.HTTP = http.DefaultClient
	}
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if filename == "" {
		filename = info.Name()
	}
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	fileID := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	pre := make([]byte, 128*1024)
	n, _ := io.ReadFull(f, pre)
	preIDBytes := sha1.Sum(pre[:n])
	preID := strings.ToUpper(hex.EncodeToString(preIDBytes[:]))
	form := url.Values{
		"file_name": {filename},
		"fileid":    {fileID},
		"preid":     {preID},
		"file_size": {strconv.FormatInt(info.Size(), 10)},
		"target":    {"U_1_" + parentID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.endpoint("/open/upload/init", openUploadInitURL), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.Creds.AccessToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			Status      int    `json:"status"`
			FileID      string `json:"file_id"`
			PickCode    string `json:"pick_code"`
			Bucket      string `json:"bucket"`
			Object      string `json:"object"`
			Callback    string `json:"callback"`
			CallbackVar string `json:"callback_var"`
			SignKey     string `json:"sign_key"`
			SignCheck   string `json:"sign_check"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !body.State {
		if body.Error == "" {
			body.Error = resp.Status
		}
		return nil, fmt.Errorf("Open API 上传初始化失败: %s", body.Error)
	}
	if (body.Data.Status == 6 || body.Data.Status == 7 || body.Data.Status == 8) && body.Data.SignKey != "" && body.Data.SignCheck != "" {
		parts := strings.SplitN(body.Data.SignCheck, "-", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("Open API 二次校验范围无效")
		}
		start, err1 := strconv.ParseInt(parts[0], 10, 64)
		end, err2 := strconv.ParseInt(parts[1], 10, 64)
		if err1 != nil || err2 != nil || start < 0 || end < start {
			return nil, fmt.Errorf("Open API 二次校验范围无效")
		}
		if _, err := f.Seek(start, 0); err != nil {
			return nil, err
		}
		rangeData := make([]byte, end-start+1)
		if _, err := io.ReadFull(f, rangeData); err != nil {
			return nil, err
		}
		rangeHash := sha1.Sum(rangeData)
		form.Set("sign_key", body.Data.SignKey)
		form.Set("sign_val", strings.ToUpper(hex.EncodeToString(rangeHash[:])))
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, u.endpoint("/open/upload/init", openUploadInitURL), bytes.NewReader([]byte(form.Encode())))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+u.Creds.AccessToken)
		req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp2, err := u.HTTP.Do(req2)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		body = struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Data  struct {
				Status      int    `json:"status"`
				FileID      string `json:"file_id"`
				PickCode    string `json:"pick_code"`
				Bucket      string `json:"bucket"`
				Object      string `json:"object"`
				Callback    string `json:"callback"`
				CallbackVar string `json:"callback_var"`
				SignKey     string `json:"sign_key"`
				SignCheck   string `json:"sign_check"`
			} `json:"data"`
		}{}
		if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
			return nil, err
		}
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 || !body.State {
			return nil, fmt.Errorf("Open API 二次校验失败: %s", body.Error)
		}
	}
	if body.Data.Status != 2 {
		if body.Data.Bucket == "" || body.Data.Object == "" {
			return nil, ErrOpenUploadRequiresDataChannel
		}
		var tokenBody struct {
			State bool `json:"state"`
			Data  struct {
				AccessKeyID     string `json:"AccessKeyId"`
				AccessKeySecret string `json:"AccessKeySecret"`
				SecurityToken   string `json:"SecurityToken"`
				Endpoint        string `json:"Endpoint"`
			} `json:"data"`
		}
		tokenReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.endpoint("/open/upload/get_token", "https://proapi.115.com/open/upload/get_token"), nil)
		if err != nil {
			return nil, err
		}
		tokenReq.Header.Set("Authorization", "Bearer "+u.Creds.AccessToken)
		tokenResp, err := u.HTTP.Do(tokenReq)
		if err != nil {
			return nil, err
		}
		defer tokenResp.Body.Close()
		if err := json.NewDecoder(tokenResp.Body).Decode(&tokenBody); err != nil {
			return nil, err
		}
		if tokenBody.Data.AccessKeyID == "" {
			return nil, fmt.Errorf("Open API 未返回 OSS 上传凭证")
		}
		endpoint := tokenBody.Data.Endpoint
		if endpoint == "" {
			endpoint = "https://oss-cn-shenzhen.aliyuncs.com"
		}
		client, err := oss.New(endpoint, tokenBody.Data.AccessKeyID, tokenBody.Data.AccessKeySecret)
		if err != nil {
			return nil, err
		}
		bucket, err := client.Bucket(body.Data.Bucket)
		if err != nil {
			return nil, err
		}
		if _, err := f.Seek(0, 0); err != nil {
			return nil, err
		}
		opts := []oss.Option{oss.SetHeader("x-oss-security-token", tokenBody.Data.SecurityToken)}
		if body.Data.Callback != "" {
			opts = append(opts, oss.Callback(body.Data.Callback))
		}
		if body.Data.CallbackVar != "" {
			opts = append(opts, oss.CallbackVar(body.Data.CallbackVar))
		}
		if err := bucket.PutObject(body.Data.Object, f, opts...); err != nil {
			return nil, err
		}
		return &UploadResult{RemoteID: body.Data.FileID, PickCode: body.Data.PickCode, SHA1: fileID, Size: info.Size(), Rapid: false}, nil
	}
	return &UploadResult{RemoteID: body.Data.FileID, PickCode: body.Data.PickCode, SHA1: fileID, Size: info.Size(), Rapid: true}, nil
}
