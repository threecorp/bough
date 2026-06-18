package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
	redisplugin "github.com/ikeikeikeike/bough/plugins/engine/redis"
)

func main() {
	port := 43503
	datadir, _ := os.MkdirTemp("", "bough-smoke-redis-")
	log.Printf("datadir: %s", datadir)
	defer os.RemoveAll(datadir)

	p := redisplugin.New()
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
	log.Println("=== ReadyCheck ===")
	ready, err := p.ReadyCheck(ctx, []int{port}, 60)
	if err != nil || !ready {
		log.Printf("ReadyCheck FAILED %s: ready=%v err=%v", time.Since(t1), ready, err)
		_ = p.Down(ctx, &api.DownReq{Ports: []int{port}, GracefulTimeoutSec: 5})
		_ = p.Cleanup(ctx, datadir, []int{port})
		os.Exit(1)
	}
	log.Printf("ReadyCheck: %s", time.Since(t1))
	fmt.Printf("\n*** redis bough-redis-%d UP+READY in %s ***\n\n", port, time.Since(t0))
	time.Sleep(2 * time.Second)
	t2 := time.Now()
	log.Println("=== Down ===")
	if err := p.Down(ctx, &api.DownReq{Ports: []int{port}, GracefulTimeoutSec: 5}); err != nil {
		log.Fatalf("Down: %v", err)
	}
	log.Printf("Down: %s", time.Since(t2))
	log.Println("=== Cleanup ===")
	_ = p.Cleanup(ctx, datadir, []int{port})
	fmt.Printf("\n*** TOTAL CYCLE: %s ***\n", time.Since(t0))
}
