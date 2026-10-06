package pan115

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	openDeviceCodeURL   = "https://passportapi.115.com/open/authDeviceCode"
	openDeviceStatusURL = "https://qrcodeapi.115.com/get/status/"
	openDeviceTokenURL  = "https://passportapi.115.com/open/deviceCodeToToken"
	openRefreshTokenURL = "https://passportapi.115.com/open/refreshToken"
)

type OpenDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	Interval        int    `json:"interval,omitempty"`
}

type OpenTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
}

// OpenAuthClient wraps the public device-code endpoints. BaseURL is exposed
// for deterministic tests and can point at a local fixture without changing
// production constants.
type OpenAuthClient struct {
	HTTP    *http.Client
	BaseURL string
}

func (c *OpenAuthClient) endpoint(path string, fallback string) string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/") + path
	}
	return fallback
}
func (c *OpenAuthClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *OpenAuthClient) StartDeviceCode(ctx context.Context, clientID string) (*OpenDeviceCode, error) {
	form := url.Values{"client_id": {clientID}}
	var raw map[string]any
	if err := c.request(ctx, http.MethodPost, c.endpoint("/open/authDeviceCode", openDeviceCodeURL), "", form, &raw); err != nil {
		return nil, err
	}
	data := responseData(raw)
	result := &OpenDeviceCode{DeviceCode: stringValue(data, "device_code", "deviceCode"), UserCode: stringValue(data, "user_code", "userCode"), VerificationURI: stringValue(data, "verification_uri", "verification_url", "verificationUri"), ExpiresIn: intValue(data, "expires_in", "expiresIn"), Interval: intValue(data, "interval")}
	if result.DeviceCode == "" {
		return nil, fmt.Errorf("Open OAuth 未返回 device_code")
	}
	if result.Interval < 1 {
		result.Interval = 5
	}
	return result, nil
}

func (c *OpenAuthClient) PollDeviceCode(ctx context.Context, deviceCode string) (*OpenTokenResponse, error) {
	q := url.Values{"device_code": {deviceCode}}
	var raw map[string]any
	if err := c.request(ctx, http.MethodGet, c.endpoint("/get/status/", openDeviceStatusURL), "", q, &raw); err != nil {
		return nil, err
	}
	data := responseData(raw)
	if token := stringValue(data, "access_token", "accessToken"); token != "" {
		return &OpenTokenResponse{AccessToken: token, RefreshToken: stringValue(data, "refresh_token", "refreshToken"), ExpiresIn: intValue(data, "expires_in", "expiresIn")}, nil
	}
	state := stringValue(data, "state", "status", "msg")
	return nil, fmt.Errorf("Open OAuth 尚未完成授权: %s", state)
}

func (c *OpenAuthClient) ExchangeDeviceCode(ctx context.Context, clientID, deviceCode string) (*OpenTokenResponse, error) {
	form := url.Values{"client_id": {clientID}, "device_code": {deviceCode}}
	var raw map[string]any
	if err := c.request(ctx, http.MethodPost, c.endpoint("/open/deviceCodeToToken", openDeviceTokenURL), "", form, &raw); err != nil {
		return nil, err
	}
	return tokenFromResponse(raw)
}

func (c *OpenAuthClient) Refresh(ctx context.Context, clientID, refreshToken string) (*OpenTokenResponse, error) {
	form := url.Values{"client_id": {clientID}, "refresh_token": {refreshToken}}
	var raw map[string]any
	if err := c.request(ctx, http.MethodPost, c.endpoint("/open/refreshToken", openRefreshTokenURL), "", form, &raw); err != nil {
		return nil, err
	}
	return tokenFromResponse(raw)
}

func (c *OpenAuthClient) request(ctx context.Context, method, endpoint, bearer string, values url.Values, out *map[string]any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Open OAuth 请求失败: %s", resp.Status)
	}
	if state, ok := (*out)["state"].(bool); ok && !state {
		return fmt.Errorf("Open OAuth 请求被拒绝")
	}
	return nil
}

func responseData(raw map[string]any) map[string]any {
	if data, ok := raw["data"].(map[string]any); ok {
		return data
	}
	return raw
}
func tokenFromResponse(raw map[string]any) (*OpenTokenResponse, error) {
	data := responseData(raw)
	result := &OpenTokenResponse{AccessToken: stringValue(data, "access_token", "accessToken"), RefreshToken: stringValue(data, "refresh_token", "refreshToken"), ExpiresIn: intValue(data, "expires_in", "expiresIn")}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("Open OAuth 未返回 access_token")
	}
	return result, nil
}
func stringValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			if text, ok := value.(string); ok {
				return text
			}
			return fmt.Sprint(value)
		}
	}
	return ""
}
func intValue(m map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			switch v := value.(type) {
			case float64:
				return int(v)
			case json.Number:
				n, _ := v.Int64()
				return int(n)
			case string:
				var n int
				fmt.Sscan(v, &n)
				return n
			}
		}
	}
	return 0
}
