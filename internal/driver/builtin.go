package driver

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// builtins are the drivers Duo ships, kept in-process until each becomes its own
// executable. Registering a Handler here is the whole integration surface a
// driver needs: the same Handler is what its eventual plugin binary serves, so
// moving it out of process is a packaging change and nothing else.
var (
	builtinMu sync.Mutex
	builtins  = map[string]func() Handler{}
)

// Register makes a driver available to `duo --agent <name>` without a plugin
// executable. It is called from the composition root, and it is the only place in
// Duo where a driver name is written down.
//
// Core reads no behaviour from this table. What a driver does comes from its
// manifest, so a name appearing here says "Duo ships this", never "Duo knows how
// this behaves".
func Register(name string, factory func() Handler) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	builtins[name] = factory
}

// BuiltinNames lists the drivers Duo ships. It is display metadata and the
// default-driver fallback: nothing in Core branches on membership.
func BuiltinNames() []string {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	out := make([]string, 0, len(builtins))
	for name := range builtins {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsBuiltin reports whether a driver is shipped in-process. `duo plugins` uses it
// to label an entry; no behaviour depends on it.
func IsBuiltin(name string) bool {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	_, ok := builtins[name]
	return ok
}

// Resolve returns a Caller for the named driver: a shipped in-process driver if
// one is registered, otherwise a plugin executable found on disk.
//
// The environment handed to a plugin process is exactly the one Core gives an
// agent, so an Agent Adapter has everything it needs to reach Duo's bridge
// without Core having to invent a second channel.
func Resolve(ctx context.Context, name string, env []string, report func(error)) (Caller, error) {
	// A plugin executable wins over an in-process built-in. That ordering is what
	// makes the move to real plugins progressive and safe in both directions:
	// installing duo-plugin-pi puts Pi on the protocol for real, and uninstalling it
	// falls back to the identical Handler running inside Duo, so `--agent pi` never
	// depends on which is present.
	if path, found := Lookup(name); found {
		// The environment handed to a plugin process is exactly the one Core gives
		// an agent, so an Agent Adapter has everything it needs to reach Duo's bridge
		// without Core inventing a second channel.
		return NewSupervised(name, SupervisedOptions{Path: path, Env: env, Report: report}), nil
	}
	builtinMu.Lock()
	factory, ok := builtins[name]
	builtinMu.Unlock()
	if ok {
		return NewBuiltin(name, factory()), nil
	}
	return nil, &NotFoundError{Driver: name}
}

// ResolveAll resolves one Caller per agent for a session, reporting a missing
// driver once with the agent it belongs to. Failing per agent rather than
// per driver keeps the message actionable when one agent is misconfigured.
func ResolveAll(ctx context.Context, names map[string]string, env []string, report func(error)) (map[string]Caller, error) {
	out := make(map[string]Caller, len(names))
	for _, agent := range sortedKeys(names) {
		name := names[agent]
		caller, err := Resolve(ctx, name, env, report)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", agent, err)
		}
		out[agent] = caller
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
