package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
	esplugin "github.com/ikeikeikeike/bough/plugins/engine/elasticsearch"
)

func main() {
	port := 43504
	datadir, _ := os.MkdirTemp("", "bough-smoke-es-")
	log.Printf("datadir: %s", datadir)
	defer os.RemoveAll(datadir)

	p := esplugin.New()
	ctx := context.Background()
	req := &api.UpReq{
		Ports:   []api.PortSpec{{Role: "main", Port: port}},
		Datadir: datadir,
		Extras:  map[string]string{"backend": "docker"},
	}
	t0 := time.Now()
	log.Println("=== Up ===")
	if err := p.Up(ctx, req); err != nil {
		log.Fatalf("Up: %v", err)
	}
	log.Printf("Up: %s", time.Since(t0))
	t1 := time.Now()
	log.Println("=== ReadyCheck (timeout 300s, JVM warmup) ===")
	ready, err := p.ReadyCheck(ctx, []int{port}, 300)
	if err != nil || !ready {
		log.Printf("ReadyCheck FAILED %s: ready=%v err=%v", time.Since(t1), ready, err)
		_ = p.Down(ctx, &api.DownReq{Ports: []int{port}, GracefulTimeoutSec: 60})
		_ = p.Cleanup(ctx, datadir, []int{port})
		os.Exit(1)
	}
	log.Printf("ReadyCheck: %s", time.Since(t1))
	fmt.Printf("\n*** elasticsearch bough-elasticsearch-%d UP+READY in %s ***\n\n", port, time.Since(t0))
	time.Sleep(2 * time.Second)
	t2 := time.Now()
	log.Println("=== Down ===")
	if err := p.Down(ctx, &api.DownReq{Ports: []int{port}, GracefulTimeoutSec: 60}); err != nil {
		log.Fatalf("Down: %v", err)
	}
	log.Printf("Down: %s", time.Since(t2))
	log.Println("=== Cleanup ===")
	_ = p.Cleanup(ctx, datadir, []int{port})
	fmt.Printf("\n*** TOTAL CYCLE: %s ***\n", time.Since(t0))
}
