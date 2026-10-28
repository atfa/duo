package main

import (
	"os"
	"time"

	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/xduo"
)

func publishStatus(svc *session.Service) func() {
	path := os.Getenv("XDUO_STATUS_FILE")
	if path == "" {
		return func() {}
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		write := func(running bool) { _ = xduo.WriteStatus(path, xduo.Snapshot(svc.View(), running)) }
		write(true)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				write(true)
			case <-done:
				write(false)
				return
			}
		}
	}()
	return func() { close(done); <-stopped }
}
