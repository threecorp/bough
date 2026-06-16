// _smoke-docker-mysql exercises the mysql plugin's Docker backend
// end-to-end without going through the bough host orchestrator. Use it
// to validate Up → ReadyCheck → Down → Cleanup against a real Docker
// daemon during v0.2 development. It is gitignored at the cmd/ root
// (underscore prefix) so GoReleaser does not ship it.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/db/api"
	mysqlplugin "github.com/ikeikeikeike/bough/plugins/db/mysql"
)

func main() {
	port := 43501
	datadir, err := os.MkdirTemp("", "bough-smoke-mysql-")
	if err != nil {
		log.Fatalf("tempdir: %v", err)
	}
	log.Printf("datadir: %s", datadir)
	defer os.RemoveAll(datadir)

	p := mysqlplugin.New()
	ctx := context.Background()

	req := api.UpReq{
		Port:             port,
		Datadir:          datadir,
		InitialDatabases: []string{"demo"},
		Extras: map[string]string{
			"backend": "docker",
		},
	}

	t0 := time.Now()
	log.Printf("=== Up ===")
	if err := p.Up(ctx, req); err != nil {
		log.Fatalf("Up: %v", err)
	}
	log.Printf("Up: %s", time.Since(t0))

	t1 := time.Now()
	log.Printf("=== ReadyCheck (timeout 180s) ===")
	ready, err := p.ReadyCheck(ctx, port, 180)
	if err != nil || !ready {
		log.Printf("ReadyCheck FAILED after %s: ready=%v err=%v", time.Since(t1), ready, err)
		log.Println("attempting Down + Cleanup before exit...")
		_ = p.Down(ctx, api.DownReq{Port: port, GracefulTimeoutSec: 30})
		_ = p.Cleanup(ctx, datadir, port)
		os.Exit(1)
	}
	log.Printf("ReadyCheck: %s", time.Since(t1))

	fmt.Printf("\n*** mysql container 'bough-mysql-%d' UP + READY in total %s ***\n\n", port, time.Since(t0))
	time.Sleep(2 * time.Second)

	t2 := time.Now()
	log.Printf("=== Down ===")
	if err := p.Down(ctx, api.DownReq{Port: port, GracefulTimeoutSec: 30}); err != nil {
		log.Fatalf("Down: %v", err)
	}
	log.Printf("Down: %s", time.Since(t2))

	t3 := time.Now()
	log.Printf("=== Cleanup ===")
	if err := p.Cleanup(ctx, datadir, port); err != nil {
		log.Fatalf("Cleanup: %v", err)
	}
	log.Printf("Cleanup: %s", time.Since(t3))

	fmt.Printf("\n*** TOTAL CYCLE: %s ***\n", time.Since(t0))
}
