package main

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
	"edu.agent.code/utils/tracing"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conf := config.InitConfig()
	logger.Init(conf.Server.LogLevel)
	//logger.Info("test a=%d, b=%s", 1, "hello")
	shutdown, err := tracing.Init(ctx, tracing.Config{
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
		if err := shutdown(ctx); err != nil {
			logger.Error("shutdown failed: %v", err)
		}
	}()

	adpt, err := adaptor.NewAdaptor(conf)
	if err != nil {
		logger.Error("new adaptor failed: %v", err)
		os.Exit(1)
	}
	fmt.Println(adpt)
}
