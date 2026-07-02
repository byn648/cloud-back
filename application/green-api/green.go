package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/yanshicheng/kube-nova/application/green-api/internal/handler"
	appcfg "github.com/yanshicheng/kube-nova/common/config"
	"github.com/yanshicheng/kube-nova/pkg/config"
)

func main() {
	cfgPath := flag.String("f", "./application/green-api/etc/green-api.yaml", "config file path")
	flag.Parse()

	var cfg appcfg.AppConfig
	config.MustLoad(*cfgPath, &cfg)

	h := handler.New(cfg)

	// 启动定时数据采集器 goroutine
	go h.SvcCtx().Green.StartDataCollector(&h.SvcCtx().GreenRepo)

	mux := http.NewServeMux()
	h.Register(mux)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	fmt.Printf("Starting %s at %s...\n", cfg.Name, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}
