package main

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/api"
	"edu.agent.code/config"
	"edu.agent.code/mcpserver"
	"edu.agent.code/router"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/tracing"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/adk"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conf := config.InitConfig()
	logger.Init(conf.Server.LogLevel)
	//logger.Info("test a=%d, b=%s", 1, "hello")
	shutdownFun, err := tracing.Init(ctx, tracing.Config{
		ServerName:     conf.Server.AppName,
		Endpoint:       conf.OTel.Endpoint,
		SampleRate:     conf.OTel.SampleRate,
		StdoutFallback: conf.OTel.StdOut,
	})

	if err != nil {
		logger.Error("init tracing failed: %v", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownFun(ctx); err != nil {
			logger.Error("shutdown failed: %v", err)
		}
	}()
	if conf.Agents.EnableChinese {
		_ = adk.SetLanguage(adk.LanguageChinese)
	}
	adpt, err := adaptor.NewAdaptor(conf)
	if err != nil {
		logger.Error("new adaptor failed: %v", err)
		os.Exit(1)
	}
	fmt.Println(adpt)

	handler, err := api.NewHandler(ctx, adpt)
	if err != nil {
		logger.Error("new api handler failed: %v", err)
		os.Exit(1)
	}
	defer func(handler *api.Handler) {
		_ = handler.Close()
	}(handler)

	app := router.New(handler)
	srv := &http.Server{
		Addr:              conf.Server.HTTPAddr,
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErrors := make(chan error, 2)
	go func() {
		logger.Info(fmt.Sprintf("http server listening at %s", srv.Addr))
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
			return
		}
		serverErrors <- nil
	}()

	go func() {
		if !conf.MCPServerSelf.Enabled {
			return
		}
		logger.Info("start mcp server on %s", conf.MCPServerSelf.HttpAddr)
		err := mcpserver.NewServer(handler.GetQuotaService(), handler.GetCostService()).ServeHTTP(conf.MCPServerSelf.HttpAddr).ListenAndServe()
		if err != nil {
			serverErrors <- err
			return
		}
		serverErrors <- nil

	}()

	select {
	case err := <-serverErrors:
		if err != nil {
			logger.Error(fmt.Sprintf("start server error: %v", err))
			os.Exit(1)
		}
	case <-ctx.Done():
		// 收到 SIGTERM/SIGINT：先优雅关闭 HTTP 服务，再关 tracing。
		// 注意：必须在超时后仍然退出，否则 systemd 只能等 90s 后 SIGKILL，
		// 会让每次部署的 restart 卡住两分钟。
		logger.Info("shutdown signal received, shutting down gracefully")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error(fmt.Sprintf("shutdown http server error: %v", err))
		}
		if err := shutdownFun(shutdownCtx); err != nil {
			logger.Error(fmt.Sprintf("shutdown tracing error: %v", err))
		}
		logger.Info("shutdown done")
	}
}
