package sessionstore

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func TestDriverStateRoundTripsWithoutLegacyFields(t *testing.T) {
	store := newTestStore(t, "session-1")
	state := json.RawMessage(`{"conversationId":"opaque-123","vendor":{"cursor":7}}`)
	want := Snapshot{SessionID: "session-1", DriverStates: map[protocol.AgentID]DriverState{protocol.Austin: {Driver: "future-driver", State: state}}}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "piSessions") {
		t.Fatalf("snapshot contains legacy piSessions: %s", data)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DriverStates[protocol.Austin].Driver != "future-driver" || !sameJSON(got.DriverStates[protocol.Austin].State, state) {
		t.Fatalf("driver state = %+v, want opaque future-driver blob", got.DriverStates[protocol.Austin])
	}
}
