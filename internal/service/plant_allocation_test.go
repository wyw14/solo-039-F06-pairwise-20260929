package service

import (
	"context"
	"testing"

	"github.com/wyw14/solo-039/internal/product"
)

func startPlantWithProof(t *testing.T, dir string) *Plant {
	t.Helper()
	plant := NewPlant(dir)
	if _, err := plant.Start(context.Background(), "gradeA", "primary"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Analyzer window needs three samples with strictly increasing timestamps.
	for _, at := range []int64{1, 2, 3} {
		if _, err := plant.AddSample(0.91, at); err != nil {
			t.Fatalf("add sample: %v", err)
		}
	}
	return plant
}

func TestPlantMultiTankAllocation(t *testing.T) {
	dir := t.TempDir()
	plant := startPlantWithProof(t, dir)

	if _, err := plant.RegisterTanks([]product.TankRegistration{
		{Name: "TK-1", Capacity: 60, Occupied: 10},
		{Name: "TK-2", Capacity: 100, Occupied: 10},
	}); err != nil {
		t.Fatalf("register tanks: %v", err)
	}

	// Total mismatch must be rejected before confirmation.
	if _, err := plant.SubmitAllocation(91, []product.ShareRequest{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}); err == nil {
		t.Fatal("allocation with mismatched total should be rejected")
	}

	if _, err := plant.SubmitAllocation(90, []product.ShareRequest{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}); err != nil {
		t.Fatalf("submit allocation: %v", err)
	}
	active, err := plant.ConfirmAllocation()
	if err != nil {
		t.Fatalf("confirm allocation: %v", err)
	}
	if active.Phase != product.AllocActive {
		t.Fatalf("phase = %s, want active", active.Phase)
	}

	// TK-1 receives its full share: complete, but the allocation stays open.
	result, err := plant.RecordDelivery("TK-1", 50)
	if err != nil {
		t.Fatalf("deliver TK-1: %v", err)
	}
	if result.ShareStatus != product.ShareComplete || result.AllocationClosed {
		t.Fatalf("unexpected delivery result: %+v", result)
	}
	if _, err := plant.RecordDelivery("TK-1", 1); err == nil {
		t.Fatal("over-delivery to completed share should be rejected")
	}
	targets := plant.ProductTargets()
	if len(targets) != 1 || targets[0] != "TK-2" {
		t.Fatalf("targets = %v, want only TK-2", targets)
	}

	// Share decisions carry full batch provenance (audit is process-local;
	// durable provenance is the persisted allocation and journal events).
	decisions := plant.ProductAudit.ForRevision(active.Revision.String())
	if len(decisions) != 2 {
		t.Fatalf("audit decisions = %d, want 2 (one per tank)", len(decisions))
	}
	for _, decision := range decisions {
		if !decision.Opened || decision.Plan.Campaign != active.Campaign || decision.Plan.Revision != active.Revision {
			t.Fatalf("share decision loses batch provenance: %+v", decision)
		}
	}

	// Restart: tanks, occupied volumes and allocation state come from the snapshot.
	restarted := NewPlant(dir)
	if err := restarted.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	snapshot := restarted.Snapshot()
	if len(snapshot.Tanks) != 2 {
		t.Fatalf("tanks = %d, want 2", len(snapshot.Tanks))
	}
	occupied := map[string]float64{}
	for _, tank := range snapshot.Tanks {
		occupied[tank.Name] = tank.Occupied
	}
	if occupied["TK-1"] != 60 {
		t.Fatalf("TK-1 occupied = %v, want 60", occupied["TK-1"])
	}
	if len(snapshot.Allocations) != 1 {
		t.Fatalf("allocations = %d, want 1", len(snapshot.Allocations))
	}
	restored := snapshot.Allocations[0]
	if restored.Revision != active.Revision || restored.Campaign != active.Campaign || restored.Grade != "gradeA" {
		t.Fatalf("batch provenance lost across restart: %+v", restored)
	}
	if restored.Phase != string(product.AllocActive) {
		t.Fatalf("restored phase = %s, want active", restored.Phase)
	}
	statuses := map[string]string{}
	actuals := map[string]float64{}
	for _, share := range restored.Shares {
		statuses[share.Tank] = share.Status
		actuals[share.Tank] = share.Actual
	}
	if statuses["TK-1"] != string(product.ShareComplete) || actuals["TK-1"] != 50 {
		t.Fatalf("TK-1 share not restored: %s %v", statuses["TK-1"], actuals["TK-1"])
	}
	if statuses["TK-2"] != string(product.ShareActive) {
		t.Fatalf("TK-2 status = %s, want active", statuses["TK-2"])
	}

	// A line break pauses only the TK-2 share; resume continues the batch.
	if err := restarted.PauseTank("TK-2", "line disconnected"); err != nil {
		t.Fatalf("pause TK-2: %v", err)
	}
	if _, err := restarted.RecordDelivery("TK-2", 10); err == nil {
		t.Fatal("delivery while paused should be rejected")
	}
	if err := restarted.ResumeTank("TK-2"); err != nil {
		t.Fatalf("resume TK-2: %v", err)
	}
	result, err = restarted.RecordDelivery("TK-2", 40)
	if err != nil {
		t.Fatalf("deliver TK-2 after resume: %v", err)
	}
	if !result.AllocationClosed {
		t.Fatal("allocation should close after the last share completes")
	}
	if alive, ok := restarted.ProductAllocator.Active(); ok {
		t.Fatalf("no allocation should be active after close: %+v", alive)
	}
}

func TestPlantConfirmWithoutEvidenceRejected(t *testing.T) {
	dir := t.TempDir()
	plant := NewPlant(dir)
	if _, err := plant.Start(context.Background(), "gradeA", "primary"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// No samples -> composition proof incomplete.
	if _, err := plant.RegisterTanks([]product.TankRegistration{{Name: "TK-1", Capacity: 100, Occupied: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.SubmitAllocation(50, []product.ShareRequest{{Tank: "TK-1", Planned: 50}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.ConfirmAllocation(); err == nil {
		t.Fatal("confirm without complete composition evidence should be rejected")
	}
}

func TestPlantGradeTransitionClosesAllocation(t *testing.T) {
	dir := t.TempDir()
	plant := startPlantWithProof(t, dir)

	if _, err := plant.RegisterTanks([]product.TankRegistration{{Name: "TK-1", Capacity: 100, Occupied: 0}}); err != nil {
		t.Fatal(err)
	}
	active, err := plant.SubmitAllocation(50, []product.ShareRequest{{Tank: "TK-1", Planned: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plant.ConfirmAllocation(); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if _, err := plant.Transition(context.Background(), "gradeB", "primary"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	closed, ok := plant.ProductAllocator.ForRevision(active.Revision.String())
	if !ok {
		t.Fatal("previous allocation must remain in history for provenance")
	}
	if closed.Phase != product.AllocClosed {
		t.Fatalf("previous allocation phase = %s, want closed", closed.Phase)
	}
	if _, ok := plant.ProductAllocator.Active(); ok {
		t.Fatal("no allocation should remain active after a grade transition")
	}
}
