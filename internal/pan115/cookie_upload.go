package pan115

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"

	ec115 "github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	"github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

const cookieUploadVersion = "36.0.1"

var uploadVersionPattern = regexp.MustCompile(`^\d+(\.\d+){1,3}$`)

// Keep appversion, its token digest and User-Agent consistent. The upstream
// driver hardcodes 27.0.5.7 in both the form and token, which 115 now rejects.
func currentUploadVersion(ctx context.Context, client *http.Client) string {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, driver.ApiGetVersion, nil)
	if err != nil {
		return cookieUploadVersion
	}
	resp, err := client.Do(req)
	if err != nil {
		return cookieUploadVersion
	}
	defer resp.Body.Close()
	var result struct {
		State bool `json:"state"`
		Data  map[string]struct {
			Version string `json:"version_code"`
		} `json:"data"`
	}
	if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) == nil && result.State {
		if version := result.Data["win"].Version; uploadVersionPattern.MatchString(version) {
			return version
		}
	}
	return cookieUploadVersion
}

type uploadCipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
	EncodeToken(int64) (string, error)
}

func cookieUploadToken(userID, fileID, fileSize, timestamp, signKey, signVal, version string) string {
	userHash := md5.Sum([]byte(userID))
	digest := md5.Sum([]byte("Qclm8MGWUv59TnrR0XPg" + fileID + fileSize + signKey + signVal + userID + timestamp + hex.EncodeToString(userHash[:]) + version))
	return hex.EncodeToString(digest[:])
}

func initCookieUpload(ctx context.Context, c *driver.Pan115Client, f io.ReadSeeker, parent, name string, size int64, digest, version string, cipher uploadCipher) (*driver.UploadInitResp, error) {
	userID, fileSize := strconv.FormatInt(c.UserID, 10), strconv.FormatInt(size, 10)
	target := "U_1_" + parent
	form := url.Values{"appid": {"0"}, "appversion": {version}, "userid": {userID}, "filename": {name}, "filesize": {fileSize}, "fileid": {digest}, "target": {target}, "sig": {c.GenerateSignature(digest, target)}, "topupload": {"true"}}
	signKey, signVal := "", ""
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		timestamp := time.Now().UnixMilli()
		key, err := cipher.EncodeToken(timestamp)
		if err != nil {
			return nil, err
		}
		form.Set("t", strconv.FormatInt(timestamp, 10))
		form.Set("token", cookieUploadToken(userID, digest, fileSize, form.Get("t"), signKey, signVal, version))
		if signKey != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		body, err := cipher.Encrypt([]byte(form.Encode()))
		if err != nil {
			return nil, err
		}
		resp, err := c.NewRequest().SetContext(ctx).SetQueryParam("k_ec", key).SetBody(body).
			SetHeader("Content-Type", "application/x-www-form-urlencoded").
			SetHeader("User-Agent", "Mozilla/5.0 115disk/"+version+" 115Browser/"+version+" 115wangpan_android/"+version).SetDoNotParseResponse(true).Post(driver.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.RawBody(), 1<<20))
		_ = resp.RawBody().Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("115 上传初始化 HTTP %d", resp.StatusCode())
		}
		plain, err := cipher.Decrypt(raw)
		if err != nil {
			return nil, err
		}
		var result driver.UploadInitResp
		if err := json.Unmarshal(plain, &result); err != nil {
			return nil, err
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		result.SHA1 = digest
		if result.Status != 7 {
			return &result, nil
		}
		var start, end int64
		if n, err := fmt.Sscanf(result.SignCheck, "%d-%d", &start, &end); err != nil || n != 2 || start < 0 || end < start || end >= size || result.SignKey == "" {
			return nil, fmt.Errorf("115 返回无效的上传校验范围")
		}
		signKey = result.SignKey
		signVal, err = c.UploadDigestRange(f, result.SignCheck)
		if err != nil {
			return nil, fmt.Errorf("读取上传校验范围失败: %w", err)
		}
	}
	return nil, fmt.Errorf("115 上传校验次数超过限制")
}

func uploadCookieFile(ctx context.Context, c *driver.Pan115Client, client *http.Client, f *os.File, parent, name string, size int64, digest string) (bool, error) {
	if ok, err := c.UploadAvailable(); err != nil {
		return false, err
	} else if !ok {
		return false, fmt.Errorf("115 上传不可用")
	}
	if c.UploadMetaInfo == nil || size > c.UploadMetaInfo.SizeLimit {
		return false, driver.ErrUploadTooLarge
	}
	version := currentUploadVersion(ctx, client)
	slog.Info("upload negotiating rapid transfer", "filename", name, "app_version", version, "total_bytes", size)
	cipher, err := ec115.NewEcdhCipher()
	if err != nil {
		return false, err
	}
	result, err := initCookieUpload(ctx, c, f, parent, name, size, digest, version, cipher)
	if err != nil {
		return false, err
	}
	rapid, err := result.Ok()
	if err != nil {
		return false, err
	}
	if rapid {
		slog.Info("upload rapid transfer succeeded", "filename", name, "upload_method", "rapid", "uploaded_bytes", 0, "total_bytes", size)
		return true, nil
	}
	slog.Info("upload requires local data transfer", "filename", name, "upload_method", "oss", "total_bytes", size)
	return false, uploadCookieMultipart(ctx, c, client, f, &result.UploadOSSParams, name, size)
}

// Stream sequential parts rather than a single PUT (which rejects files over
// 5 GiB), preserving constant memory use and allowing cancellation per part.
func uploadCookieMultipart(ctx context.Context, c *driver.Pan115Client, client *http.Client, f *os.File, params *driver.UploadOSSParams, name string, size int64) error {
	if params.Bucket == "" || params.Object == "" {
		return fmt.Errorf("115 未返回 OSS 上传位置")
	}
	token, err := c.GetOSSToken()
	if err != nil {
		return err
	}
	options := []oss.ClientOption{oss.SecurityToken(token.SecurityToken), oss.Timeout(30, 300)}
	if client != nil {
		dataClient := *client
		dataClient.Timeout = 5 * time.Minute
		options = append(options, oss.HTTPClient(&dataClient))
	}
	ossClient, err := oss.New("https://"+c.GetOSSEndpoint(false), token.AccessKeyID, token.AccessKeySecret, options...)
	if err != nil {
		return err
	}
	bucket, err := ossClient.Bucket(params.Bucket)
	if err != nil {
		return err
	}
	init, err := bucket.InitiateMultipartUpload(params.Object, oss.WithContext(ctx), oss.Sequential())
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			_ = bucket.AbortMultipartUpload(init, oss.WithContext(context.Background()))
		}
	}()
	parts := []oss.UploadPart{}
	partSize := int64(16 << 20)
	if size/partSize >= 10000 {
		partSize = (size + 9998) / 9999
	}
	lastLog := time.Time{}
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(partSize, size-offset)
		part, err := bucket.UploadPart(init, io.NewSectionReader(f, offset, n), n, len(parts)+1, oss.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("OSS 分片 %d 上传失败: %w", len(parts)+1, err)
		}
		parts = append(parts, part)
		offset += n
		if time.Since(lastLog) >= 30*time.Second || offset == size {
			slog.Info("upload data progress", "filename", name, "upload_method", "oss", "uploaded_bytes", offset, "total_bytes", size)
			lastLog = time.Now()
		}
	}
	completeOptions := append(driver.OssOption(params, token), oss.WithContext(ctx))
	if _, err := bucket.CompleteMultipartUpload(init, parts, completeOptions...); err != nil {
		return err
	}
	completed = true
	return nil
}
