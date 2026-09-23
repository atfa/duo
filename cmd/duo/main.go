package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/tui"
	"github.com/atfa/duo/internal/workspace"
)

const version = "v0.3.1"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println("Duo " + version)
			return
		case "-h", "--help", "help":
			fmt.Println("Duo " + version)
			fmt.Println("Usage: duo [git-repository]")
			fmt.Println("Run with no path from inside a Git repository: cd project && duo")
			return
		}
	}
	cfg := loadConfig(os.Args[1:])
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: cfg.repository,
		Root:       cfg.worktreeRoot,
		Session:    cfg.session,
		BaseRef:    cfg.baseRef,
	})
	set, err := ws.Prepare(ctx)
	if err != nil {
		log.Fatalf("prepare Duo worktrees: %v", err)
	}

	state := project.NewState()
	tracker := harness.NewTracker()
	bus := events.NewBus()
	token, err := sessionToken()
	if err != nil {
		log.Fatalf("create Duo session token: %v", err)
	}
	server := transport.NewServer(cfg.listen, cfg.session, token)
	coord := coordinator.New(server, state, tracker, ws, bus)
	server.SetHandler(coord)

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe(ctx) }()
	select {
	case <-server.Ready():
	case err := <-serverErr:
		if err != nil {
			log.Fatal(err)
		}
		return
	case <-ctx.Done():
		return
	}

	host, port := bridgeAddress(server.Addr())
	agents := agent.NewManager()
	agents.Add(agent.NewSession(agent.Config{Agent: protocol.Austin, Dir: set.Austin.Path, Host: host, Port: port, Session: cfg.session, Token: token, Command: cfg.piCommand}))
	agents.Add(agent.NewSession(agent.Config{Agent: protocol.Tony, Dir: set.Tony.Path, Host: host, Port: port, Session: cfg.session, Token: token, Command: cfg.piCommand}))
	if err := agents.StartAll(ctx); err != nil {
		log.Fatal(err)
	}
	defer agents.StopAll()

	if cfg.harnessEnabled {
		monitor := harness.NewMonitor(cfg.harness, state, tracker, server, bus)
		go monitor.Run(ctx)
	}

	app := tui.New(coord, state, tracker, ws, server, agents, bus)
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Duo TUI: %v", err)
	}
	cancel()
	agents.StopAll()

	fmt.Printf("\nDuo stopped. Worktrees preserved in %s\n", set.Root)
}

func sessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func bridgeAddress(listen string) (string, string) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "127.0.0.1", "8765"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return host, port
}
