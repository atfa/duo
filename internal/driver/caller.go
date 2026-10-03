package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Caller is how Duo Core talks to a driver. A Client speaks it over a pipe to a
// plugin process; a Builtin speaks it by calling a Handler in the same process.
// Core never knows which, because the two are the same contract — that is what
// lets a built-in driver become an out-of-process plugin without Core changing.
type Caller interface {
	// Name is the driver name, as selected on the command line.
	Name() string
	Describe(ctx context.Context) (*Manifest, error)
	Probe(ctx context.Context) (*ProbeResult, error)
	Prepare(ctx context.Context, req LaunchRequest) (*LaunchPlan, error)
	// State is the driver's current resume blob, which can differ from the one
	// Prepare returned because the agent learned its identity after launch. A
	// driver with nothing new to report answers ErrUnsupported and Core keeps
	// the blob it already has.
	State(ctx context.Context) (json.RawMessage, error)
	Models(ctx context.Context) (*ModelList, error)
	Thinking(ctx context.Context) (*ThinkingOptions, error)
	// Close releases the driver. A plugin process exits; a built-in stops any
	// background work it started.
	Close()
}

// Builtin serves a Handler in Core's own process. It exists so a driver Duo
// ships can be introduced through exactly the same interface as a third-party
// plugin, one milestone before it becomes an out-of-process executable.
//
// Both paths go through the same optional-method checks, so a Builtin cannot
// accidentally answer a method its manifest does not declare.
type Builtin struct {
	name    string
	handler Handler

	mu       sync.Mutex
	man      *Manifest
	describe error
}

// NewBuiltin wraps a Handler as a Caller. describe is called once and cached,
// because Core needs the capabilities on every delivery decision and a plugin
// must not be able to change them mid-session.
func NewBuiltin(name string, h Handler) *Builtin {
	return &Builtin{name: name, handler: h}
}

func (b *Builtin) Name() string { return b.name }

// Describe returns the cached manifest. A failure is cached too, but as the
// error it was: caching a bad manifest would hide it, and re-running describe on
// every delivery decision would let a driver change its capabilities halfway
// through a session.
func (b *Builtin) Describe(ctx context.Context) (*Manifest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.man != nil {
		copied := *b.man
		return &copied, nil
	}
	if b.describe != nil {
		return nil, b.describe
	}
	man, err := b.handler.Describe()
	if err == nil {
		err = ValidateManifest(man)
	}
	if err != nil {
		b.describe = fmt.Errorf("driver plugin %s: %w", b.name, err)
		return nil, b.describe
	}
	b.man = man
	copied := *man
	return &copied, nil
}

func (b *Builtin) Probe(ctx context.Context) (*ProbeResult, error) {
	return b.handler.Probe()
}

func (b *Builtin) Prepare(ctx context.Context, req LaunchRequest) (*LaunchPlan, error) {
	plan, err := b.handler.Prepare(req)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.Command == "" {
		return nil, fmt.Errorf("driver plugin %s: prepare returned no command", b.name)
	}
	return plan, nil
}

func (b *Builtin) State(ctx context.Context) (json.RawMessage, error) {
	provider, ok := b.handler.(StateProvider)
	if !ok {
		return nil, fmt.Errorf("driver plugin %s: %s: %w", b.name, MethodState, ErrUnsupported)
	}
	return provider.State()
}

func (b *Builtin) Models(ctx context.Context) (*ModelList, error) {
	lister, ok := b.handler.(ModelLister)
	if !ok {
		return nil, fmt.Errorf("driver plugin %s: %s: %w", b.name, MethodModels, ErrUnsupported)
	}
	return lister.Models()
}

func (b *Builtin) Thinking(ctx context.Context) (*ThinkingOptions, error) {
	provider, ok := b.handler.(ThinkingProvider)
	if !ok {
		return nil, fmt.Errorf("driver plugin %s: %s: %w", b.name, MethodThinking, ErrUnsupported)
	}
	return provider.Thinking()
}

// Close releases background work the Handler started. It is optional: a driver with
// nothing to release need not implement it.
//
// A driver that watches a file or runs a goroutine has no other way to stop, and an
// in-process driver has no process exit to stop it for it.
func (b *Builtin) Close() {
	if closer, ok := b.handler.(io.Closer); ok {
		closer.Close()
	}
}

// Supervised wraps a Caller so a crashed plugin process is replaced rather than
// taking the session with it. A plugin is a separate process: if it dies, Duo's
// collaboration loop still has work to do, and an operator watching the TUI must
// not see a session that has frozen because a helper exited.
//
// Every crash is reported once, through Report, so the reason is on screen and in
// the log instead of silently disappearing behind a retry, and the very next call
// runs against a fresh process.
type Supervised struct {
	name string

	mu   sync.Mutex
	path string
	env  []string
	live *Client

	open   func(ctx context.Context, path string, env []string) (*Client, error)
	report func(error)
}

// SupervisedOptions configures a Supervised caller.
type SupervisedOptions struct {
	// Path is the plugin executable to (re)start.
	Path string
	// Env is the environment the plugin process gets.
	Env []string
	// Open starts a plugin process. Tests replace it; nil uses Start.
	Open func(ctx context.Context, path string, env []string) (*Client, error)
	// Report is called once per crash, with the reason. nil discards it.
	Report func(error)
}

// NewSupervised returns a Caller that lazily starts a plugin process and
// respawns it after a crash.
func NewSupervised(name string, opts SupervisedOptions) *Supervised {
	open := opts.Open
	if open == nil {
		open = func(ctx context.Context, path string, env []string) (*Client, error) {
			return Start(ctx, name, path, nil, env)
		}
	}
	return &Supervised{name: name, path: opts.Path, env: opts.Env, open: open, report: opts.Report}
}

func (s *Supervised) Name() string { return s.name }

// client returns a live plugin process, starting one if the previous exited.
func (s *Supervised) client(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	if s.live != nil {
		select {
		case <-s.live.done:
			s.live = nil
		default:
			c := s.live
			s.mu.Unlock()
			return c, nil
		}
	}
	s.mu.Unlock()

	c, err := s.open(ctx, s.path, s.env)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.live = c
	s.mu.Unlock()
	return c, nil
}

// discard forgets a process Core has given up on, so the next call starts a new
// one, and reports the crash that forced the change.
func (s *Supervised) discard(c *Client, cause error) {
	s.mu.Lock()
	if s.live == c {
		s.live = nil
	}
	s.mu.Unlock()
	if s.report != nil && cause != nil {
		s.report(cause)
	}
	go c.Close()
}

// invoke runs one call, replacing a process that died mid-call so the caller's
// next attempt gets a working plugin.
func (s *Supervised) invoke(ctx context.Context, do func(*Client) error) error {
	c, err := s.client(ctx)
	if err != nil {
		return err
	}
	if err := do(c); err != nil {
		if isCrash(err) {
			s.discard(c, fmt.Errorf("driver plugin %s: %w", s.name, err))
		}
		return err
	}
	return nil
}

// Describe returns the manifest, revalidating it after a respawn so a plugin that
// comes back in a different or broken state is caught where Core can still say so
// plainly.
func (s *Supervised) Describe(ctx context.Context) (*Manifest, error) {
	var man *Manifest
	if err := s.invoke(ctx, func(c *Client) error {
		out, err := c.Describe(ctx)
		if err != nil {
			return err
		}
		man = out
		return nil
	}); err != nil {
		return nil, err
	}
	return man, nil
}

func (s *Supervised) Probe(ctx context.Context) (*ProbeResult, error) {
	var out *ProbeResult
	err := s.invoke(ctx, func(c *Client) error {
		res, err := c.Probe(ctx)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Supervised) Prepare(ctx context.Context, req LaunchRequest) (*LaunchPlan, error) {
	var out *LaunchPlan
	err := s.invoke(ctx, func(c *Client) error {
		res, err := c.Prepare(ctx, req)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Supervised) State(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.invoke(ctx, func(c *Client) error {
		res, err := c.State(ctx)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Supervised) Models(ctx context.Context) (*ModelList, error) {
	var out *ModelList
	err := s.invoke(ctx, func(c *Client) error {
		res, err := c.Models(ctx)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Supervised) Thinking(ctx context.Context) (*ThinkingOptions, error) {
	var out *ThinkingOptions
	err := s.invoke(ctx, func(c *Client) error {
		res, err := c.Thinking(ctx)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Supervised) Close() {
	s.mu.Lock()
	c := s.live
	s.live = nil
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// isCrash reports whether err means the plugin process is gone rather than that
// it answered with a refusal.
func isCrash(err error) bool {
	return err != nil && errors.Is(err, ErrCrashed)
}

// manifestCapabilities is a small helper for the delivery decisions Core makes on
// every message. Reading a capability should never be the place a nil manifest
// becomes a panic, so this fails closed.
func manifestCapabilities(man *Manifest) Capabilities {
	if man == nil {
		return Capabilities{}
	}
	return man.Capabilities
}

// StateJSON is a small helper for the one place Core touches a plugin's opaque
// blob: turning a stored value back into something to hand to prepare.
func StateJSON(raw string) json.RawMessage {
	if raw == "" {
		return nil
	}
	return json.RawMessage(raw)
}
