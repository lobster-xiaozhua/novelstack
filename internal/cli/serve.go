package cli

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"novelstack/internal/app"
	"novelstack/internal/config"
)

// serve 启动 HTTP 服务；装配逻辑全部在 internal/app，演练/CLI 复用同一套。
func serve(cfg *config.Config, initialPassword string) int {
	log.SetFlags(log.LstdFlags | log.LUTC)

	a, err := app.New(context.Background(), app.Options{
		Config:          cfg,
		InitialPassword: initialPassword,
	})
	if err != nil {
		log.Printf("startup: %v", err)
		return 1
	}
	defer a.Close()
	a.StartBackground()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		// 朗读是长连接流式响应，写超时按最大片段数放宽（逐段 flush，不依赖整章完成）
		WriteTimeout: 30 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("listening on %s (secure_cookie=%v, max_upload=%d bytes, kek=v%d)",
			cfg.Addr, cfg.CookieSecure, cfg.MaxUploadBytes, cfg.KEKVersion)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http: %v", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Printf("bye")
	return 0
}
