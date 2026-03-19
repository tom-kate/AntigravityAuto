package main

import (
	"antiauto/api"
	"antiauto/config"
	"antiauto/db"
	L "antiauto/logger"
	"antiauto/server"
	"fmt"
	"log"
)

func main() {
	L.Sys("加载配置...")
	if err := config.Load(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Start hot reload watcher
	config.StartHotReload()

	L.Sys("初始化数据库...")
	if err := db.Init("data.db"); err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}

	L.Sys("登录短信平台...")
	if err := api.SMSLogin(); err != nil {
		L.Warn("system", fmt.Sprintf("短信平台登录失败: %v", err))
	}

	cfg := config.Get()
	L.Sys(fmt.Sprintf("启动 Web 服务 → http://localhost:%d", cfg.Port))
	if cfg.Headless {
		L.Sys("浏览器模式: 无头(后台)")
	} else {
		L.Sys("浏览器模式: 有头(可见)")
	}

	if err := server.Start(cfg.Port); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
