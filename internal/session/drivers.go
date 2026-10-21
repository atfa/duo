package session

import (
	"context"

	"github.com/atfa/duo/internal/driver"
)

// Drivers returns discovered driver choices without probing or executing plugins.
func (s *Service) Drivers(ctx context.Context) ([]DriverChoice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := driver.Discover()
	choices := make([]DriverChoice, 0, len(entries))
	for _, entry := range entries {
		choices = append(choices, DriverChoice{Name: entry.Name, Shipped: entry.Shipped})
	}
	return choices, nil
}
