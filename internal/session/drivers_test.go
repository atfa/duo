package session

import (
	"context"
	"reflect"
	"testing"

	"github.com/atfa/duo/internal/driver"
)

func TestDriversProjectsDiscoveryWithoutProbing(t *testing.T) {
	choices, err := (&Service{}).Drivers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := make([]DriverChoice, 0)
	for _, entry := range driver.Discover() {
		want = append(want, DriverChoice{Name: entry.Name, Shipped: entry.Shipped})
	}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("drivers = %#v, want %#v", choices, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Service{}).Drivers(ctx); err == nil {
		t.Fatal("Drivers accepted cancelled context")
	}
}
