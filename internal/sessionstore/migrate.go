package sessionstore

// Snapshot migrations are versioned steps, not a compatibility shim. Every
// released schema version keeps the step that upgrades it to the next one, so a
// binary that publishes a new schema can still read the sessions written before
// it instead of stranding them. A version without a complete chain to the
// current one is still refused: guessing would silently reinterpret a session's
// signatures and queue.

// migrations maps a persisted schema version to the step that upgrades a
// snapshot written with it to the next version.
var migrations = map[int]func(Snapshot) Snapshot{
	2: migrateV2ToV3,
}

// hasMigrationPath reports whether this binary can read a snapshot written with
// version, either directly or by migrating it one step at a time.
func hasMigrationPath(version int) bool {
	if version < 1 || version > SchemaVersion {
		return false
	}
	for v := version; v < SchemaVersion; v++ {
		if _, ok := migrations[v]; !ok {
			return false
		}
	}
	return true
}

// SchemaCompatible reports whether a snapshot written with version can be read
// by this binary. It is the same rule Load applies, exposed so a check that only
// has a version number — a delivered checkout, a listing — can ask it too.
func SchemaCompatible(version int) bool { return hasMigrationPath(version) }

// Migrate upgrades snap to the current schema in memory and records the version
// it was read with. It reports false for a version this binary cannot read; the
// caller then refuses with SchemaError.
func Migrate(snap Snapshot) (Snapshot, bool) {
	from := snap.SchemaVersion
	if !hasMigrationPath(from) {
		return snap, false
	}
	for v := from; v < SchemaVersion; v++ {
		snap = migrations[v](snap)
		snap.SchemaVersion = v + 1
	}
	snap.PersistedSchemaVersion = from
	return snap, true
}

// migrateV2ToV3 writes down what v2 left implicit. v2 omitted an unset mode and
// an unset per-step policy and left every reader to default them, so a later
// default could silently reinterpret an old session's workflow and queue. v3
// records both once, with the meaning the v2 snapshot already had, so the
// signatures and the queue survive the upgrade unchanged.
func migrateV2ToV3(snap Snapshot) Snapshot {
	snap.Mode = snap.EffectiveMode()
	for i := range snap.Steps {
		snap.Steps[i].Policy = snap.Steps[i].EffectivePolicy()
	}
	return snap
}
