package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"

	"example.com/isureal-company/isureal"
)

// 2026-09-23

func main() {
	// 計測用。localhost だけで待ち受けるので外からは見えない（scripts/bench-monitor.sh が CPU プロファイルを取る）
	go func() {
		log.Println(http.ListenAndServe("127.0.0.1:6060", nil))
	}()

	config := isureal.ConfigFromEnv()
	server := isureal.NewServer(config)
	if err := server.Start(":8080"); err != nil {
		panic("failed to start server: " + err.Error())
	}
}
