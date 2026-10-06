package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/local/115-direct/internal/config"
	"github.com/local/115-direct/internal/httpapi"
	"github.com/local/115-direct/internal/jellyfin"
	"github.com/local/115-direct/internal/netproxy"
	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/syncer"
	"github.com/local/115-direct/internal/tmdb"
	"github.com/local/115-direct/internal/uploader"
	"github.com/local/115-direct/internal/wecom"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:9527/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		_ = resp.Body.Close()
		return
	}
	_ = syscall.Umask(0o077)
	cfg := config.Load()
	if strings.TrimSpace(cfg.AdminPassword) == "" {
		panic("ADMIN_PASSWORD must be set in .env or the service environment")
	}
	if err := cfg.Prepare(); err != nil {
		panic(err)
	}
	vault, err := secure.LoadOrCreate(cfg.MasterKeyPath())
	if err != nil {
		panic(err)
	}
	secret, err := os.ReadFile(cfg.MasterKeyPath())
	if err != nil {
		panic(err)
	}
	st, err := store.Open(cfg.DatabasePath(), vault)
	if err != nil {
		panic(err)
	}
	defer st.Close()
	configureLogging(st)
	pan := pan115.New()
	pan.ConfigureHTTP(&http.Client{Transport: netproxy.New(st, "pan"), Timeout: 30 * time.Second})
	var panCfg struct {
		Cookie string `json:"cookie"`
	}
	if st.GetSetting(context.Background(), "pan", &panCfg) == nil && panCfg.Cookie != "" {
		if err := pan.SetCookie(panCfg.Cookie); err != nil {
			slog.Warn("stored cookie rejected", "error", err)
		}
	}

	tmdbClient := tmdb.New(func(ctx context.Context) (string, error) {
		var value struct {
			Token string `json:"token"`
		}
		err := st.GetSetting(ctx, "tmdb", &value)
		return value.Token, err
	})
	jellyLoader := func(ctx context.Context) (jellyfin.Config, error) {
		var value jellyfin.Config
		err := st.GetSetting(ctx, "jellyfin", &value)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err == nil {
			err = value.Resolve()
		}
		return value, err
	}
	jellyClient := jellyfin.NewClient(jellyLoader)
	tmdbClient.HTTP.Transport = netproxy.New(st, "tmdb")
	tmdbClient.Cache = st
	tmdbClient.ImageHTTP = &http.Client{Transport: netproxy.New(st, "images"), Timeout: 30 * time.Second}
	jellyClient.HTTP.Transport = netproxy.New(st, "jellyfin")
	jobs := organize.NewService(st, pan, tmdbClient, secret, organize.DirectoryConfig{STRMPath: cfg.STRMPath(), PendingPath: cfg.PendingDir, GatewayURL: cfg.PublicURL})
	jobs.Refresh = jellyClient.Refresh
	wecomLoader := func(ctx context.Context) (wecom.Config, error) {
		var value wecom.Config
		err := st.GetSetting(ctx, "wecom", &value)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return value, err
	}
	wecomHandler := wecom.NewHandler(st, jobs, wecomLoader)
	wecomHandler.HTTP.Transport = netproxy.New(st, "wecom")
	jobs.Notify = func(ctx context.Context, user, message string) {
		if err := wecomHandler.Send(ctx, user, message); err != nil {
			slog.Warn("WeCom notify failed", "error", err)
		}
	}
	cmsConfig := jellyfin.DefaultCMSConfig()
	if err := st.GetSetting(context.Background(), "cms", &cmsConfig); err != nil && !errors.Is(err, sql.ErrNoRows) {
		panic(err)
	}
	migrationReadOnly := cmsConfig.MigrationReadOnly
	if !migrationReadOnly {
		jobs.Start(context.Background())
	}
	syncService := syncer.New(st, pan, secret, organize.DirectoryConfig{STRMPath: cfg.STRMPath(), PendingPath: cfg.PendingDir, GatewayURL: cfg.PublicURL})
	syncService.Refresh = jellyClient.Refresh
	syncService.LibraryMu = jobs.LibraryMutex()
	syncService.OnChanged = func(ctx context.Context) error {
		if err := jobs.QueuePendingLocal(ctx); err != nil {
			return err
		}
		_, err := jobs.SubmitScrape(ctx)
		return err
	}
	wecomHandler.Sync = syncService
	if !migrationReadOnly {
		syncService.Start(context.Background())
	}
	var uploadCfg uploader.Config
	if err := st.GetSetting(context.Background(), "upload", &uploadCfg); err != nil {
		uploadCfg = uploader.Config{LocalDir: cfg.UploadDir, Channel: "auto", ScanInterval: 30, StableSeconds: 30}
	} else if uploadCfg.LocalDir == "" {
		uploadCfg.LocalDir = cfg.UploadDir
	}
	uploadService := uploader.New(st, pan, uploadCfg)
	uploadService.OpenHTTP = &http.Client{Transport: netproxy.New(st, "pan"), Timeout: 30 * time.Second}
	uploadService.OnUploaded = jobs.IngestUploaded
	if !migrationReadOnly {
		uploadService.Start(context.Background())
	}

	auth, err := httpapi.NewAuth(cfg.AdminUser, cfg.AdminPassword, secret, cfg.SessionTTL, strings.HasPrefix(cfg.PublicURL, "https://"))
	if err != nil {
		panic(err)
	}
	var authCfg struct {
		PasswordHash string `json:"password_hash"`
	}
	if err := st.GetSetting(context.Background(), "auth", &authCfg); err == nil && authCfg.PasswordHash != "" {
		if err := auth.LoadPasswordHash(authCfg.PasswordHash); err != nil {
			panic(err)
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		panic(err)
	}
	if err := st.EnsureUser(context.Background(), cfg.AdminUser, auth.PasswordHash(), "admin"); err != nil {
		panic(err)
	}
	auth.SetUserLookup(func(username string) (httpapi.AuthUser, error) {
		user, err := st.GetUser(context.Background(), username)
		if err != nil {
			return httpapi.AuthUser{}, err
		}
		return httpapi.AuthUser{Username: user.Username, PasswordHash: user.PasswordHash, Role: user.Role, Enabled: user.Enabled}, nil
	})
	gateway := jellyfin.NewGateway(st, pan, secret, jellyLoader)
	gateway.BackendTransport = netproxy.New(st, "jellyfin")
	gateway.BackendHTTP = &http.Client{Transport: gateway.BackendTransport, Timeout: 15 * time.Second}
	cmsRuntime := &jellyfin.CMSRuntime{Store: st}
	gateway.CMSLoader = cmsRuntime.Bridge
	api := &httpapi.Server{CMS: cmsRuntime, CMSListen: cfg.CMSLegacyAddr, MigrationReadOnly: migrationReadOnly, Store: st, Pan: pan, Jobs: jobs, TMDB: tmdbClient, Jellyfin: jellyClient, Gateway: gateway, WeCom: wecomHandler, Auth: auth, Sync: syncService, Upload: uploadService, STRMRoot: cfg.STRMPath()}
	if !migrationReadOnly {
		if err := api.RecoverUploadSessions(context.Background()); err != nil {
			slog.Error("upload session recovery", "error", err)
		}
	}
	adminServer := &http.Server{Addr: cfg.ListenAddr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	gatewayServer := &http.Server{Addr: cfg.GatewayAddr, Handler: gateway, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	legacyServer := &http.Server{Addr: cfg.CMSLegacyAddr, Handler: cmsRuntime, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go serve("cms-legacy", legacyServer)
	go serve("admin", adminServer)
	go serve("gateway", gatewayServer)
	slog.Info("115 Direct started", "admin", cfg.ListenAddr, "gateway", cfg.GatewayAddr)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = adminServer.Shutdown(ctx)
	_ = gatewayServer.Shutdown(ctx)
	_ = legacyServer.Shutdown(ctx)
}

func configureLogging(st *store.Store) {
	// SetDefault redirects log.Print to slog, so the sink must not use the default log handler.
	slog.SetDefault(slog.New(&store.LogHandler{Store: st, Next: slog.NewTextHandler(os.Stderr, nil)}))
}

func serve(name string, server *http.Server) {
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error(name+" server stopped", "error", err)
		os.Exit(1)
	}
}
