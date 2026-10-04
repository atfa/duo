package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/version"
)

// versionString is the Duo build recorded in checkpoints and transcript headers.
func versionString() string { return version.Version }

// composeSnapshot builds the durable snapshot. When coord is nil the integration
// result is empty, which is exactly right for a fresh session.
func (s *Service) composeSnapshot(coord *coordinator.Coordinator) sessionstore.Snapshot {
	integration := s.integration
	deliveryState := s.delivery
	if coord != nil {
		integration = coord.CurrentIntegration()
		deliveryState = coord.CurrentDelivery()
	}
	driverStates := make(map[protocol.AgentID]sessionstore.DriverState, len(s.driverState))
	for k, v := range s.driverState {
		driverStates[k] = v
	}
	piSessions := make(map[protocol.AgentID]string, len(s.piSessions))
	for k, v := range s.piSessions {
		if strings.TrimSpace(v) != "" {
			piSessions[k] = v
		}
	}
	agentDrivers := make(map[protocol.AgentID]string)
	agentModels := make(map[protocol.AgentID]string)
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if s.opts.AgentDrivers[id] != "" {
			agentDrivers[id] = s.opts.AgentDrivers[id]
		}
		if s.opts.AgentModels[id] != "" {
			agentModels[id] = s.opts.AgentModels[id]
		}
	}
	if s.agents != nil {
		for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			d, ok := s.agents.Driver(id)
			if !ok {
				continue
			}
			if dt := d.DriverType(); dt != "" {
				agentDrivers[id] = dt
			}
			if m := d.Model(); m != "" {
				agentModels[id] = m
			}
			// What the driver learned this run wins over anything loaded, or a
			// driver that learned its identity would keep replaying the previous one.
			if state := d.DriverState(); len(state) > 0 {
				if dt := d.DriverType(); dt != "" {
					driverStates[id] = sessionstore.DriverState{Driver: dt, State: state}
				}
			}
			// The mirror follows the identity the driver reports — the same value
			// v0.9.0 persisted — never a field read out of the blob.
			if sid := d.SessionID(); sid != "" {
				piSessions[id] = sid
			}
		}
	}
	if coord != nil {
		for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if m := coord.Model(id); m != "" {
				agentModels[id] = m
			}
		}
	}
	return recovery.Compose(recovery.ComposeInput{
		DuoVersion:   versionString(),
		SessionID:    s.sessionID,
		RepoID:       s.repoID,
		Repository:   s.set.Repository,
		BaseBranch:   s.set.BaseBranch,
		BaseCommit:   s.set.BaseCommit,
		CreatedAt:    s.createdAt,
		Project:      s.state.Snapshot(),
		Worktrees:    s.set,
		PiSessions:   piSessions,
		DriverStates: driverStates,
		AgentDrivers: agentDrivers,
		AgentModels:  agentModels,
		Integration:  integration,
		Delivery:     deliveryState,
	})
}

// sessionToken mints the shared secret every agent must present to reach the bridge.
func sessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// bridgeAddress splits the listen address into the host and port an agent is told
// to dial. A wildcard bind is normalised to loopback, because an agent runs on the
// same machine and 0.0.0.0 is not an address it can dial.
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

// driverResolve finds the driver for an agent: one Duo ships in-process, or a
// plugin executable on disk.
func driverResolve(ctx context.Context, name string, env []string, onErr func(error)) (driver.Caller, error) {
	return driver.Resolve(ctx, name, env, onErr)
}

// manifestOf asks a driver to describe itself. A driver that cannot is treated as
// having no manifest, which means its own defaults stand.
func manifestOf(ctx context.Context, plugin driver.Caller) *driver.Manifest {
	manifest, err := plugin.Describe(ctx)
	if err != nil {
		return nil
	}
	return manifest
}

// exitReason describes a failed process in one clause, for a headline above the
// driver's own output.
func exitReason(event agent.LifecycleEvent) string {
	if event.Err != nil {
		return event.Err.Error()
	}
	return event.State.String()
}

// indentBlock keeps a multi-line driver message readable in the session log
// without letting its later lines look like unrelated entries.
func indentBlock(text string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n  ")
}

// gitChanges lists what a worktree changed relative to base: a diffstat plus any
// untracked files.
//
// It lives behind the session rather than in a renderer because it runs external
// commands: a frontend that drew it directly would be doing I/O from inside a
// paint, which is what the terminal interface used to do.
func gitChanges(ctx context.Context, dir, base string) []string {
	if dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var lines []string
	args := []string{"-C", dir, "diff", "--stat"}
	if base != "" {
		args = append(args, base)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.Output(); err == nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			lines = append(lines, strings.Split(trimmed, "\n")...)
		}
	}

	ctx2, cancel2 := context.WithTimeout(ctx, 2*time.Second)
	defer cancel2()
	cmd2 := exec.CommandContext(ctx2, "git", "-C", dir, "status", "--porcelain")
	if out2, err := cmd2.Output(); err == nil {
		var untracked []string
		for _, raw := range strings.Split(string(out2), "\n") {
			line := strings.TrimRight(raw, "\r")
			if strings.HasPrefix(line, "?? ") {
				untracked = append(untracked, strings.TrimPrefix(line, "?? "))
			}
		}
		for _, name := range untracked {
			lines = append(lines, "?? "+name)
		}
	}
	return lines
}
