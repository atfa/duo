package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/atfa/duo/internal/clidoc"
	"github.com/atfa/duo/internal/mcp"
	"github.com/atfa/duo/internal/protocol"
)

type mcpServerArgs struct {
	cfg        mcp.Config
	exportJSON bool
	help       bool
}

func parseMCPServerArgs(args []string) (mcpServerArgs, error) {
	var out mcpServerArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case arg == "config" && i == 0:
			out.exportJSON = true
		case arg == "--agent" || arg == "-a":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("flag --agent requires an argument (austin|tony)")
			}
			out.cfg.Agent = protocol.CanonicalAgent(args[i])
		case strings.HasPrefix(arg, "--agent=") || strings.HasPrefix(arg, "-a="):
			out.cfg.Agent = protocol.CanonicalAgent(strings.SplitN(arg, "=", 2)[1])
		case arg == "--session" || arg == "-s":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("flag --session requires an argument")
			}
			out.cfg.SessionID = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--session=") || strings.HasPrefix(arg, "-s="):
			out.cfg.SessionID = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case arg == "--token" || arg == "-k":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("flag --token requires an argument")
			}
			out.cfg.Token = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--token=") || strings.HasPrefix(arg, "-k="):
			out.cfg.Token = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case arg == "--host" || arg == "-H":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("flag --host requires an argument")
			}
			out.cfg.Host = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--host=") || strings.HasPrefix(arg, "-H="):
			out.cfg.Host = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case arg == "--port" || arg == "-p":
			i++
			if i >= len(args) {
				return out, fmt.Errorf("flag --port requires an argument")
			}
			out.cfg.Port = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--port=") || strings.HasPrefix(arg, "-p="):
			out.cfg.Port = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case arg == "-h" || arg == "--help" || arg == "help":
			printMCPHelp()
			out.help = true
			return out, nil
		default:
			return out, fmt.Errorf("unknown flag %q", arg)
		}
	}
	return out, nil
}

func printMCPHelp() {
	if command, ok := clidoc.Lookup("mcp-server"); ok {
		fmt.Println("Usage: " + command.Signature)
	} else {
		fmt.Println("Usage: duo mcp-server [flags]")
	}
	fmt.Println()
	fmt.Println("Runs the Model Context Protocol (MCP) server over stdio for Duo state machine tools.")
	fmt.Println("Connection parameters default to DUO_AGENT, DUO_SESSION, DUO_TOKEN, DUO_HOST, DUO_PORT environment variables.")
	fmt.Println()
	fmt.Println("Connection flags:")
	fmt.Println("  --agent, -a <austin|tony> Agent role (default: $DUO_AGENT)")
	fmt.Println("  --session, -s <id>       Duo session ID (default: $DUO_SESSION)")
	fmt.Println("  --token, -k <token>      Duo session token (default: $DUO_TOKEN)")
	fmt.Println("  --host, -H <host>        Duo bridge host (default: $DUO_HOST or 127.0.0.1)")
	fmt.Println("  --port, -p <port>        Duo bridge port (default: $DUO_PORT)")
	fmt.Println("  duo mcp-server config    Print MCP client configuration JSON instead of starting server")
}

func runMCPServer(ctx context.Context, args []string) error {
	parsed, err := parseMCPServerArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		return nil
	}

	// Fill unset fields from environment variables
	parsed.cfg.LoadConfigFromEnv()

	if parsed.exportJSON {
		return exportMCPConfig(parsed.cfg)
	}

	if parsed.cfg.Host == "" {
		parsed.cfg.Host = "127.0.0.1"
	}
	if parsed.cfg.Port == "" {
		return fmt.Errorf("missing bridge port: pass --port or set DUO_PORT")
	}
	if parsed.cfg.SessionID == "" {
		return fmt.Errorf("missing session ID: pass --session or set DUO_SESSION")
	}
	if parsed.cfg.Token == "" {
		return fmt.Errorf("missing session token: pass --token or set DUO_TOKEN")
	}
	if parsed.cfg.Agent == "" {
		return fmt.Errorf("missing agent role: pass --agent (austin|tony) or set DUO_AGENT")
	}

	client, err := mcp.ConnectBridge(ctx, parsed.cfg)
	if err != nil {
		return fmt.Errorf("mcp bridge connection failed: %w", err)
	}
	defer client.Close()

	server := mcp.NewServer(client)
	return server.Serve(os.Stdin, os.Stdout)
}

func exportMCPConfig(cfg mcp.Config) error {
	exe, err := os.Executable()
	if err != nil {
		exe = "duo"
	}

	mcpArgs := []string{"mcp-server"}
	if cfg.Agent != "" {
		mcpArgs = append(mcpArgs, "--agent", string(cfg.Agent))
	}
	if cfg.SessionID != "" {
		mcpArgs = append(mcpArgs, "--session", cfg.SessionID)
	}
	if cfg.Token != "" {
		mcpArgs = append(mcpArgs, "--token", cfg.Token)
	}
	if cfg.Host != "" {
		mcpArgs = append(mcpArgs, "--host", cfg.Host)
	}
	if cfg.Port != "" {
		mcpArgs = append(mcpArgs, "--port", cfg.Port)
	}

	config := map[string]any{
		"mcpServers": map[string]any{
			"duo": map[string]any{
				"command": exe,
				"args":    mcpArgs,
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
