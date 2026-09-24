package main

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/api"
	"edu.agent.code/config"
	"edu.agent.code/router"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/tracing"
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

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(fmt.Sprintf("http server listening at %s", srv.Addr))
		err := srv.ListenAndServe()
		if err != nil {
			serverErrors <- err
			os.Exit(1)
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

		logger.Info("shutdown timeout ")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownFun(shutdownCtx); err != nil {
			logger.Error(fmt.Sprintf("shutdown timeout after %s", conf.Server.HTTPAddr))
			os.Exit(1)
		}
		if err := <-serverErrors; err != nil {
			logger.Error(fmt.Sprintf("shutdown server error: %v", err))
			os.Exit(1)
		}
	}
}
