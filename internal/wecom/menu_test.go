package wecom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSyncMenuCreatesEnabledButtonsAndDeletesWhenDisabled(t *testing.T) {
	cfg := Config{Enabled: true, CorpID: "corp", AgentID: "1", Secret: "secret", MenuEnabled: true, MenuOrganize: true, MenuFullSync: true, MenuIncrementalSync: true}
	var paths []string
	var payload map[string]any
	h := NewHandler(nil, nil, func(context.Context) (Config, error) { return cfg, nil })
	h.HTTP = &http.Client{Transport: messageTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path == "/cgi-bin/menu/create" {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
		}
		body := `{"errcode":0,"errmsg":"ok"}`
		if r.URL.Path == "/cgi-bin/gettoken" {
			body = `{"errcode":0,"access_token":"fixture","expires_in":7200}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if err := h.SyncMenu(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasPrefix(paths[0], "/cgi-bin/gettoken?") || !strings.HasPrefix(paths[1], "/cgi-bin/menu/create?") {
		t.Fatalf("unexpected menu calls: %v", paths)
	}
	if !strings.Contains(paths[1], "agentid=1") {
		t.Fatalf("agent ID missing from menu call: %v", paths)
	}
	buttons, ok := payload["button"].([]any)
	if !ok || len(buttons) != 3 {
		t.Fatalf("unexpected buttons: %#v", payload)
	}
	if buttons[0].(map[string]any)["key"] != "115_organize" || buttons[1].(map[string]any)["key"] != "115_full_sync" || buttons[2].(map[string]any)["key"] != "115_incremental_sync" {
		t.Fatalf("button order changed: %#v", buttons)
	}
	cfg.MenuEnabled = false
	paths = nil
	if err := h.SyncMenu(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], "/cgi-bin/menu/delete?") || !strings.Contains(paths[0], "agentid=1") {
		t.Fatalf("unexpected delete calls: %v", paths)
	}
}

func TestMenuValidationRequiresButton(t *testing.T) {
	if err := (Config{MenuEnabled: true}).Validate(); err == nil {
		t.Fatal("menu without buttons accepted")
	}
}
