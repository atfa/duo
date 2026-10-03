package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// defaultTimeout bounds a single call. Model listing shells out to another CLI,
// which can take seconds to start, so this is generous enough for the slowest
// method and short enough that a wedged plugin cannot freeze the TUI.
const defaultTimeout = 20 * time.Second

// StartTimeout is the deadline StartTimeout-based callers ask for. The contract
// suite drives real agent CLIs, whose model listing shells out again and can take
// longer than an interactive call should ever block; a test that reports a slow
// machine as a protocol violation is worse than a slow test.
const StartTimeout = 90 * time.Second

// Client is Core's handle on one plugin process. It is safe for concurrent use:
// several goroutines may call different methods while the reader goroutine
// dispatches replies.
type Client struct {
	name string
	// timeout bounds one call. Zero means defaultTimeout.
	timeout time.Duration

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	nextID  int64
	waitErr error
	closed  bool

	pendingMu sync.Mutex
	pending   map[int64]chan *Response

	done chan struct{}
	// died guards closing done, which both the process waiter and the read loop can
	// reach: a plugin that exits and a pipe that reaches EOF are the same event
	// observed twice.
	died sync.Once
}

// dead records that this plugin can no longer answer, once.
func (c *Client) dead() {
	c.died.Do(func() { close(c.done) })
}

// Start launches the plugin executable and begins reading its replies. The
// process stays up for the lifetime of the returned Client: the plugin may hold
// state across calls and may attach its Agent Adapter at any point.
func Start(ctx context.Context, name, path string, args []string, env []string) (*Client, error) {
	return start(ctx, name, path, args, env, defaultTimeout)
}

// StartWithTimeout is Start with a per-call deadline of the caller's choosing. Only
// the contract suite needs it, because it drives plugins whose methods shell out to
// a real agent CLI.
func StartWithTimeout(ctx context.Context, name, path string, args, env []string, timeout time.Duration) (*Client, error) {
	return start(ctx, name, path, args, env, timeout)
}

func start(ctx context.Context, name, path string, args []string, env []string, timeout time.Duration) (*Client, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start plugin %s: %w", name, err)
	}
	// stderr stays attached to Core's own stderr so a plugin's diagnostics are
	// visible without a second log path.
	cmd.Stderr = defaultStderr()
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start plugin %s: %w", name, err)
	}

	c := &Client{
		name:    name,
		timeout: timeout,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[int64]chan *Response),
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.waitErr = err
		c.mu.Unlock()
		c.failPending(ErrCrashed)
		c.dead()
	}()
	return c, nil
}

// Name is the plugin's driver name, as Core selected it.
func (c *Client) Name() string { return c.name }

// pluginStderr is where a plugin's own diagnostics go. A test replaces it to
// capture them; by default they land on Core's stderr, where the operator
// already looks, rather than in a second log path.
var pluginStderr io.Writer = os.Stderr

func defaultStderr() io.Writer { return pluginStderr }

func (c *Client) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	// A describe result with a long notice list, or a model catalog, is one
	// line but can be far larger than bufio's default 64 KiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			// A plugin that writes something other than protocol JSON on
			// stdout is broken, but one bad line must not kill the session: the
			// pending call reports a timeout and the plugin's own stderr carries
			// the reason.
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.pendingMu.Unlock()
		if ok {
			ch <- &resp
		}
	}
	// EOF with no more replies: every outstanding call is now unanswerable, and so
	// is every call that has not been made yet. Recording that matters as much as
	// failing the pending ones — a plugin that was already gone when a call started
	// would otherwise find an empty pending map, register into the fresh one, and
	// wait out the full timeout for a reply that can never come.
	c.failPending(ErrCrashed)
	c.dead()
}

func (c *Client) failPending(cause error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan *Response)
	c.pendingMu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	_ = cause
}

// Call invokes one method and decodes its result into out. A nil out discards
// the result. It returns ErrUnsupported when the plugin declares the method
// unimplemented, and ErrCrashed when the plugin process died or never replied.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode %s params: %w", method, err)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("driver plugin %s: %w", c.name, ErrCrashed)
	}
	if err := c.waitErr; err != nil {
		c.mu.Unlock()
		return fmt.Errorf("driver plugin %s exited: %v: %w", c.name, err, ErrCrashed)
	}
	c.nextID++
	id := c.nextID
	req := Request{Protocol: ProtocolVersion, ID: id, Method: method, Params: raw}
	line, err := json.Marshal(req)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("encode %s request: %w", method, err)
	}
	ch := make(chan *Response, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		c.mu.Unlock()
		return fmt.Errorf("driver plugin %s: write %s: %w: %w", c.name, method, err, ErrCrashed)
	}
	c.mu.Unlock()

	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return fmt.Errorf("driver plugin %s: %s timed out after %s: %w", c.name, method, timeout, ctx.Err())
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return fmt.Errorf("driver plugin %s: %s: %w", c.name, method, ErrCrashed)
		}
		return decodeResponse(c.name, method, resp, out)
	case <-c.done:
		return fmt.Errorf("driver plugin %s: %s: %w", c.name, method, ErrCrashed)
	}
}

func decodeResponse(name, method string, resp *Response, out any) error {
	if resp.Protocol != 0 && resp.Protocol != ProtocolVersion {
		return fmt.Errorf("driver plugin %s: %s answered with protocol version %d, Core speaks %d", name, method, resp.Protocol, ProtocolVersion)
	}
	if !resp.OK {
		rpcErr := resp.Error
		if rpcErr == nil {
			rpcErr = &RPCError{Code: CodeInternal, Message: "no error detail"}
		}
		if rpcErr.Code == CodeUnsupported || rpcErr.Code == CodeUnknownMethod {
			return fmt.Errorf("driver plugin %s: %s: %w", name, method, ErrUnsupported)
		}
		return fmt.Errorf("driver plugin %s: %s: %w", name, method, rpcErr)
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("driver plugin %s: decode %s result: %w", name, method, err)
	}
	return nil
}

// Describe asks the plugin to describe itself. It is the first call Core makes
// and the only source of driver behaviour.
func (c *Client) Describe(ctx context.Context) (*Manifest, error) {
	var out Manifest
	if err := c.Call(ctx, MethodDescribe, nil, &out); err != nil {
		return nil, err
	}
	if err := ValidateManifest(&out); err != nil {
		return nil, fmt.Errorf("driver plugin %s: %w", c.name, err)
	}
	return &out, nil
}

// Probe reports whether the underlying agent CLI is usable.
func (c *Client) Probe(ctx context.Context) (*ProbeResult, error) {
	var out ProbeResult
	if err := c.Call(ctx, MethodProbe, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Prepare turns Core state into a runnable launch specification.
func (c *Client) Prepare(ctx context.Context, req LaunchRequest) (*LaunchPlan, error) {
	var out LaunchPlan
	if err := c.Call(ctx, MethodPrepare, req, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Command) == "" {
		return nil, fmt.Errorf("driver plugin %s: prepare returned no command", c.name)
	}
	return &out, nil
}

// State asks the plugin what its resume blob is right now. A plugin that
// implements nothing beyond the required methods answers ErrUnsupported, which
// is a normal outcome and not a failure.
func (c *Client) State(ctx context.Context) (json.RawMessage, error) {
	var out StateResult
	if err := c.Call(ctx, MethodState, nil, &out); err != nil {
		return nil, err
	}
	return out.State, nil
}

// Models lists the catalog, and is only valid when Capabilities.Models is set.
func (c *Client) Models(ctx context.Context) (*ModelList, error) {
	var out ModelList
	if err := c.Call(ctx, MethodModels, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Thinking lists the reasoning-effort levels, and is only valid when
// Capabilities.Thinking is set.
func (c *Client) Thinking(ctx context.Context) (*ThinkingOptions, error) {
	var out ThinkingOptions
	if err := c.Call(ctx, MethodThinking, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Close asks the plugin to shut down and terminates the process. It is safe to
// call more than once.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	// A well-behaved plugin exits on EOF; a broken one must not block shutdown.
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		c.kill()
	}
}

func (c *Client) kill() {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Wait blocks until the plugin process exits.
func (c *Client) Wait() { <-c.done }

// ValidateManifest rejects a manifest Core cannot act on. A plugin that
// describes itself badly must fail here, at one place, rather than producing a
// confusing failure later in a session.
func ValidateManifest(m *Manifest) error {
	if m == nil {
		return errors.New("manifest is empty")
	}
	if m.Protocol != ProtocolVersion {
		return fmt.Errorf("manifest declares protocol version %d; Core speaks %d", m.Protocol, ProtocolVersion)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("manifest has no name")
	}
	switch m.Capabilities.Resume {
	case ResumeClient, ResumeServer, ResumeNone:
	case "":
		return fmt.Errorf("manifest %q does not declare a resume mode", m.Name)
	default:
		return fmt.Errorf("manifest %q declares unknown resume mode %q", m.Name, m.Capabilities.Resume)
	}
	switch m.Capabilities.Bridge {
	case BridgeNone, BridgeAgent, BridgePlugin:
	case "":
		return fmt.Errorf("manifest %q does not declare who owns its bridge endpoint", m.Name)
	default:
		return fmt.Errorf("manifest %q declares unknown bridge owner %q", m.Name, m.Capabilities.Bridge)
	}
	switch m.ModelReference {
	case ModelQualified, ModelBare:
	default:
		return fmt.Errorf("manifest %q declares unknown model reference format %q", m.Name, m.ModelReference)
	}
	// Core may deliver an injected message over the bridge only when something on
	// the other end can act on it. A plugin-owned endpoint observes the agent
	// from outside and cannot write to the PTY Duo Core owns, so a manifest
	// claiming live steering there would look healthy in describe and then
	// silently swallow every human prompt, harness nudge and steer.
	if m.Capabilities.LiveSteering && m.Capabilities.Bridge != BridgeAgent {
		return fmt.Errorf("manifest %q claims liveSteering but its bridge is %q; only an agent-owned bridge can receive an injected message", m.Name, m.Capabilities.Bridge)
	}
	return nil
}
