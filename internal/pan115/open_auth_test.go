package pan115

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAuthDeviceAndToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/authDeviceCode":
			fmt.Fprint(w, `{"state":true,"data":{"device_code":"dev-1","user_code":"U1","expires_in":600}}`)
		case "/open/deviceCodeToToken":
			fmt.Fprint(w, `{"state":true,"data":{"access_token":"access","refresh_token":"refresh","expires_in":3600}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &OpenAuthClient{BaseURL: server.URL}
	device, err := c.StartDeviceCode(context.Background(), "client")
	if err != nil || device.DeviceCode != "dev-1" {
		t.Fatalf("device: %#v %v", device, err)
	}
	token, err := c.ExchangeDeviceCode(context.Background(), "client", device.DeviceCode)
	if err != nil || token.AccessToken != "access" || token.RefreshToken != "refresh" {
		t.Fatalf("token: %#v %v", token, err)
	}
}
