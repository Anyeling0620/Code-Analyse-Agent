package main

import (
	"edu.agent.code/config"
	"edu.agent.code/utils/logger"
)

func main() {
	conf := config.InitConfig()
	logger.Init(conf.Server.LogLevel)
	logger.Info("test a=%d, b=%s", 1, "hello")
}
