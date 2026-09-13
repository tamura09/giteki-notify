package main

import (
	"context"
	"testing"
	"time"
)

func TestChangesReportsOnlyUnseenNumbers(t *testing.T) {
	state := newState()
	state.Targets["ubiquiti"] = targetState{Certificates: map[string]certificateState{
		"201-250579": {Date: "2026-08-14", RecordCount: 1},
	}}

	certificates := groupByNumber([]record{
		testRecord("020-250266", "2026-08-25", "ＵＣ－Ｃａｓｔ－Ｐｒｏ", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
		testRecord("201-250579", "2026-08-14", "Ｕ７－Ｐｒｏ－ＸＧＳ", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
	})

	changes := state.changes("ubiquiti", certificates)

	if len(changes) != 1 {
		t.Fatalf("reported %d changes, want 1: %+v", len(changes), changes)
	}
	if changes[0].Certificate.Number != "020-250266" || changes[0].Kind != changeNew {
		t.Errorf("change = %s %s, want 020-250266 new", changes[0].Certificate.Number, changes[0].Kind)
	}
}

func TestChangesReportsOldestFirst(t *testing.T) {
	state := newState()
	state.Targets["ubiquiti"] = targetState{Certificates: map[string]certificateState{}}

	// groupByNumber hands back newest first, because that is the order the
	// register returns. Posting follows the order things were granted in.
	certificates := groupByNumber([]record{
		testRecord("020-250266", "2026-08-25", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
		testRecord("201-250579", "2026-08-14", "Ｕ７", "第２条第１９号に規定する特定無線設備", "Ｇ１Ｄ"),
	})

	changes := state.changes("ubiquiti", certificates)

	if len(changes) != 2 {
		t.Fatalf("reported %d changes, want 2", len(changes))
	}
	if changes[0].Certificate.Number != "201-250579" {
		t.Errorf("first reported = %s, want the older 201-250579", changes[0].Certificate.Number)
	}
}

func TestChangesReportsAnAmendedCertificate(t *testing.T) {
	state := newState()
	state.Targets["ubiquiti"] = targetState{Certificates: map[string]certificateState{
		"020-250266": {Date: "2026-08-25", RecordCount: 2},
	}}

	// The register added a third 工事設計 to a number already seen.
	changes := state.changes("ubiquiti", groupByNumber(ucCastPro()))

	if len(changes) != 1 {
		t.Fatalf("reported %d changes, want 1: %+v", len(changes), changes)
	}
	if changes[0].Kind != changeAmended {
		t.Errorf("kind = %s, want amended", changes[0].Kind)
	}
	if changes[0].PreviousRecordCount != 2 {
		t.Errorf("PreviousRecordCount = %d, want 2", changes[0].PreviousRecordCount)
	}
}

func TestChangesSaysNothingWhenTheRecordCountIsUnchanged(t *testing.T) {
	state := newState()
	state.Targets["ubiquiti"] = targetState{Certificates: map[string]certificateState{
		"020-250266": {Date: "2026-08-25", RecordCount: 3},
	}}

	if changes := state.changes("ubiquiti", groupByNumber(ucCastPro())); len(changes) != 0 {
		t.Fatalf("reported %+v, want nothing", changes)
	}
}

func TestRecordKeepsTheOriginalFirstSeenAt(t *testing.T) {
	state := newState()
	earlier := testNow.Add(-72 * time.Hour)
	state.Targets["ubiquiti"] = targetState{Certificates: map[string]certificateState{
		"020-250266": {Date: "2026-08-25", RecordCount: 2, FirstSeenAt: earlier},
	}}

	certificates := groupByNumber(ucCastPro())
	state.record("ubiquiti", searchResult{LastUpdateDate: "2026-09-11"}, certificates, testNow)

	stored := state.Targets["ubiquiti"].Certificates["020-250266"]
	if !stored.FirstSeenAt.Equal(earlier) {
		t.Errorf("FirstSeenAt = %s, want the original %s", stored.FirstSeenAt, earlier)
	}
	if stored.RecordCount != 3 {
		t.Errorf("RecordCount = %d, want the current 3", stored.RecordCount)
	}
	if got := state.Targets["ubiquiti"].LastUpdateDate; got != "2026-09-11" {
		t.Errorf("LastUpdateDate = %q, want 2026-09-11", got)
	}
	if !state.Targets["ubiquiti"].LastCheckedAt.Equal(testNow) {
		t.Errorf("LastCheckedAt = %s, want %s", state.Targets["ubiquiti"].LastCheckedAt, testNow)
	}
}

func TestRecordStoresTheTypeNameForAReader(t *testing.T) {
	state := newState()
	state.record("ubiquiti", searchResult{}, groupByNumber(ucCastPro()), testNow)

	if got := state.Targets["ubiquiti"].Certificates["020-250266"].TypeName; got != "UC-Cast-Pro" {
		t.Errorf("stored TypeName = %q, want UC-Cast-Pro", got)
	}
}

func TestPruneDropsTargetsThatAreNoLongerConfigured(t *testing.T) {
	state := newState()
	state.Targets["ubiquiti"] = targetState{}
	state.Targets["retired"] = targetState{}

	state.prune([]target{{ID: "ubiquiti", ApplicantName: "Ubiquiti"}})

	if _, ok := state.Targets["retired"]; ok {
		t.Error("retired target survived prune")
	}
	if _, ok := state.Targets["ubiquiti"]; !ok {
		t.Error("configured target was pruned")
	}
}

func TestSeenDistinguishesAFirstRunFromAnEmptyResult(t *testing.T) {
	state := newState()
	if state.seen("ubiquiti") {
		t.Error("a state with no record claims to have seen the target")
	}

	// A target whose search legitimately matched nothing still gets a record, so
	// the run after it reports its first certification rather than seeding it.
	state.record("ubiquiti", searchResult{}, nil, testNow)
	if !state.seen("ubiquiti") {
		t.Error("a recorded target with no certificates reads as a first run")
	}
}

func TestLoadStateTreatsAMissingObjectAsAFirstRun(t *testing.T) {
	client := newTestApp()
	client.objects = newFakeObjects()

	state, err := client.loadState(context.Background(), "bucket", defaultStateKey)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if state == nil || len(state.Targets) != 0 {
		t.Fatalf("state = %+v, want an empty one", state)
	}
}

func TestSaveStateThenLoadStateRoundTrips(t *testing.T) {
	objects := newFakeObjects()
	client := newTestApp()
	client.objects = objects

	state := newState()
	state.record("ubiquiti", searchResult{LastUpdateDate: "2026-09-11"}, groupByNumber(ucCastPro()), testNow)

	if err := client.saveState(context.Background(), "bucket", defaultStateKey, state); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	reloaded, err := client.loadState(context.Background(), "bucket", defaultStateKey)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}

	stored := reloaded.Targets["ubiquiti"].Certificates["020-250266"]
	if stored.RecordCount != 3 || stored.Date != "2026-08-25" {
		t.Errorf("reloaded certificate = %+v, want 3 records dated 2026-08-25", stored)
	}

	// A run that changed nothing must write the same bytes, so the bucket's
	// version history only shows real changes.
	first := objects.objects[defaultStateKey]
	if err := client.saveState(context.Background(), "bucket", defaultStateKey, reloaded); err != nil {
		t.Fatalf("saveState again: %v", err)
	}
	if string(objects.objects[defaultStateKey]) != string(first) {
		t.Error("re-saving an unchanged state produced different bytes")
	}
}
