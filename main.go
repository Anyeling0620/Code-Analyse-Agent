package main

import (
	"edu.agent.code/config"
	"fmt"
)

func main() {
	conf := config.InitConfig()
	fmt.Println(conf)
}
