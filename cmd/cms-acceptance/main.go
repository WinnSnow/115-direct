// cms-acceptance starts only the isolated gateway and read-only report server.
// It never starts sync, uploader, organizer, cleanup or WeCom workers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/115-direct/internal/jellyfin"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

func main() {
	data := flag.String("data", "/data", "isolated test data directory")
	gatewayAddr := flag.String("gateway", ":9096", "test gateway listen address")
	adminAddr := flag.String("admin", ":9527", "test report listen address")
	backend := flag.String("backend", "http://jellyfin.example.test:8091", "Jellyfin upstream")
	cms := flag.String("cms", "http://cms.example.test:9527", "CMS upstream")
	public := flag.String("public", "http://127.0.0.1:29096", "browser gateway URL")
	mode := flag.String("mode", "cms-upstream", "cms-upstream or independent-115")
	legacyAddr := flag.String("legacy", "", "isolated legacy /d/ listener; independent mode only")
	flag.Parse()
	if *mode != "cms-upstream" && *mode != "independent-115" {
		log.Fatal("invalid test mode")
	}
	if err := os.MkdirAll(*data, 0o700); err != nil {
		log.Fatal(err)
	}
	vault, err := secure.LoadOrCreate(filepath.Join(*data, "master.key"))
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(filepath.Join(*data, "acceptance.db"), vault)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.PutSetting(context.Background(), "playback", jellyfin.PlaybackOptions{PublicURL: *public}); err != nil {
		log.Fatal(err)
	}
	secret, err := os.ReadFile(filepath.Join(*data, "master.key"))
	if err != nil {
		log.Fatal(err)
	}
	gate := jellyfin.NewGateway(st, nil, secret, func(context.Context) (jellyfin.Config, error) { return jellyfin.Config{URL: *backend}, nil })
	var bridge *jellyfin.CMSBridge
	var guard *reuseOnlyTransport
	origins := []string{*cms}
	if *mode == "independent-115" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pan, transport, picks, loadErr := independentPan(ctx, *data, vault)
		cancel()
		if loadErr != nil {
			log.Fatal(loadErr)
		}
		guard = transport
		bridge, err = jellyfin.NewIndependentCMSBridge(origins, picks, pan.DownloadURL, 16, 5*time.Second)
	} else {
		bridge, err = jellyfin.NewCMSBridge(*cms, origins, 20, 3*time.Second)
	}
	if err != nil {
		log.Fatal(err)
	}
	gate.CMS = bridge
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			result := map[string]any{"status": "running", "mode": bridge.Mode(), "cloud_mutations": false, "workers": false, "link_requests": bridge.Requests()}
			if guard != nil {
				requests, blocked := guard.counts()
				result["pan_read_requests"] = requests
				result["pan_blocked_requests"] = blocked
				result["cms_requests"] = 0
				result["pan_login_enabled"] = false
			} else {
				result["upstream_requests"] = bridge.Requests()
				result["last_upstream_status"] = bridge.LastStatus()
			}
			json.NewEncoder(w).Encode(result)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(*data, "report.html"))
		})
		log.Fatal((&http.Server{Addr: *adminAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
	}()
	if *legacyAddr != "" {
		if guard == nil {
			log.Fatal("legacy test listener requires independent mode")
		}
		go func() {
			log.Fatal((&http.Server{Addr: *legacyAddr, Handler: http.HandlerFunc(bridge.ServeLegacyHTTP), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
		}()
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			path := strings.ToLower(r.URL.Path)
			allowed := r.Method == http.MethodPost && (path == "/users/authenticatebyname" || path == "/sessions/logout" || strings.HasPrefix(path, "/sessions/playing") || strings.HasPrefix(path, "/sessions/capabilities") || strings.HasSuffix(path, "/playbackinfo"))
			if !allowed {
				http.Error(w, "acceptance gateway blocks remote writes", http.StatusForbidden)
				return
			}
		}
		gate.ServeHTTP(w, r)
	})
	log.Printf("isolated acceptance gateway started; mode=%s; background workers disabled", bridge.Mode())
	log.Fatal((&http.Server{Addr: *gatewayAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}
