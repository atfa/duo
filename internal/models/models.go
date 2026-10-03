// Package models holds a driver's model catalog.
//
// The catalog comes from the driver over the plugin protocol, never from a command
// Duo builds itself: which model ids exist, how a reference is spelled and what the
// default is are all things only the driver knows. A driver that does not publish a
// catalog says so with its capabilities, and Core reports that rather than guessing.
package models

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/driver"
)

// Model is one selectable model. Provider and ID together identify it
// unambiguously, because a model ID may itself contain a provider prefix.
type Model struct {
	Provider string
	ID       string
	Thinking bool
	Images   bool
	// Bare reports that the driver declared modelReference: bare, meaning it takes
	// the id on its own and strips any provider prefix itself. It is carried on the
	// model rather than passed to Reference so that the reference a picker displays
	// cannot disagree with the one the driver will be sent.
	Bare bool
}

// Reference is the canonical provider/id form, used for display and filtering.
func (m Model) Reference() string {
	if m.Bare || m.Provider == "" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}

// Source is anything a catalog can be read from. A live session satisfies it, and so
// does a driver resolved without one, which is what makes the catalog testable
// without an agent process.
type Source interface {
	Manifest() *driver.Manifest
	Capabilities() driver.Capabilities
	Models(ctx context.Context) (*driver.ModelList, error)
}

// Load asks the driver for its catalog. A driver that does not publish one is an
// answer, not a failure of the call: the error says which driver and that it
// publishes none, because "the picker is empty" and "the picker is broken" are
// different things to show a human.
func Load(ctx context.Context, src Source) ([]Model, error) {
	if src == nil {
		return nil, fmt.Errorf("no driver to read a model catalog from")
	}
	manifest := src.Manifest()
	name := "driver"
	if manifest != nil && manifest.Name != "" {
		name = manifest.Name
	}
	if !src.Capabilities().Models {
		return nil, fmt.Errorf("driver %s publishes no model catalog", name)
	}
	list, err := src.Models(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, nil
	}
	bare := manifest != nil && manifest.ModelReference == driver.ModelBare
	out := make([]Model, 0, len(list.Models))
	for _, m := range list.Models {
		out = append(out, Model{
			Provider: m.Provider,
			ID:       m.ID,
			Thinking: m.Thinking,
			Images:   m.Images,
			Bare:     bare,
		})
	}
	return out, nil
}

// Default is the model a driver runs with when the operator chose none, which is
// whatever its manifest declares. A driver with nothing to declare returns "" and
// its agent resolves its own choice, which is the correct outcome rather than a
// gap to fill in from Core.
func Default(manifest *driver.Manifest) string {
	if manifest == nil {
		return ""
	}
	return strings.TrimSpace(manifest.DefaultModel)
}

// Apply folds a possibly-bare reference into the form the driver declared it takes,
// so a model persisted for one driver cannot reach another and be rejected by it. A
// qualified reference that arrives without a provider is left alone: Core cannot
// invent a provider it was never told about.
func Apply(reference, format string) string {
	ref := strings.TrimSpace(reference)
	if ref == "" || format != driver.ModelBare {
		return ref
	}
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		return ref[slash+1:]
	}
	return ref
}
