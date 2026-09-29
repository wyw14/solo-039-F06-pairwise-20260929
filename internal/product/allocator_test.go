package product

import (
	"testing"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

func newTestAllocator(t *testing.T) (*Allocator, *TankBook) {
	t.Helper()
	book := NewTankBook()
	allocator := NewAllocator(book)
	if _, err := allocator.RegisterTanks([]TankRegistration{
		{Name: "TK-1", Capacity: 60, Occupied: 10},
		{Name: "TK-2", Capacity: 100, Occupied: 10},
	}); err != nil {
		t.Fatalf("register tanks: %v", err)
	}
	return allocator, book
}

func testPlan(total float64, shares ...Share) Allocation {
	return Allocation{
		Campaign:      uuid.New(),
		Revision:      uuid.New(),
		Grade:         "gradeA",
		ExpectedTotal: total,
		Shares:        shares,
	}
}

func TestAllocatorSubmitRejectsTotalMismatch(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	plan := testPlan(91, Share{Tank: "TK-1", Planned: 50}, Share{Tank: "TK-2", Planned: 40})
	if _, err := allocator.Submit(plan); err == nil {
		t.Fatal("expected total mismatch rejected")
	}

	plan = testPlan(90+1e-9, Share{Tank: "TK-1", Planned: 50}, Share{Tank: "TK-2", Planned: 40})
	if _, err := allocator.Submit(plan); err != nil {
		t.Fatalf("tolerance-level difference should be accepted: %v", err)
	}

	plan = testPlan(90.001, Share{Tank: "TK-1", Planned: 50}, Share{Tank: "TK-2", Planned: 40})
	if _, err := allocator.Submit(plan); err == nil {
		t.Fatal("difference beyond tolerance should be rejected")
	}
}

func TestAllocatorSubmitRejectsInvalidPlan(t *testing.T) {
	allocator, _ := newTestAllocator(t)

	cases := []struct {
		name string
		plan Allocation
	}{
		{"empty shares", testPlan(0)},
		{"negative planned", testPlan(50, Share{Tank: "TK-1", Planned: -1})},
		{"duplicate tank", testPlan(100, Share{Tank: "TK-1", Planned: 50}, Share{Tank: "TK-1", Planned: 50})},
		{"unknown tank", testPlan(50, Share{Tank: "TK-X", Planned: 50})},
		{"capacity exceeded", testPlan(51, Share{Tank: "TK-1", Planned: 51})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := allocator.Submit(tc.plan); err == nil {
				t.Fatalf("%s: expected rejection", tc.name)
			}
		})
	}
}

func TestAllocatorRegisterRejectsInvalidTanks(t *testing.T) {
	allocator := NewAllocator(NewTankBook())
	cases := []struct {
		name  string
		items []TankRegistration
	}{
		{"empty", nil},
		{"zero capacity", []TankRegistration{{Name: "TK-1", Capacity: 0}}},
		{"negative occupied", []TankRegistration{{Name: "TK-1", Capacity: 10, Occupied: -1}}},
		{"occupied over capacity", []TankRegistration{{Name: "TK-1", Capacity: 10, Occupied: 11}}},
		{"duplicate in request", []TankRegistration{{Name: "TK-1", Capacity: 10}, {Name: "TK-1", Capacity: 20}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := allocator.RegisterTanks(tc.items); err == nil {
				t.Fatalf("%s: expected rejection", tc.name)
			}
		})
	}
}

func confirmPlan(t *testing.T, allocator *Allocator) Allocation {
	t.Helper()
	plan := testPlan(90, Share{Tank: "TK-1", Planned: 50}, Share{Tank: "TK-2", Planned: 40})
	draft, err := allocator.Submit(plan)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	active, err := allocator.Confirm(draft.Campaign, draft.Revision)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	return active
}

func shareStatus(allocation Allocation, tank string) ShareStatus {
	for _, share := range allocation.Shares {
		if share.Tank == tank {
			return share.Status
		}
	}
	return ""
}

func TestAllocatorDeliverFillsShares(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	active := confirmPlan(t, allocator)

	result, _, err := allocator.Deliver("TK-1", 50)
	if err != nil {
		t.Fatalf("deliver TK-1: %v", err)
	}
	if result.ShareStatus != ShareComplete {
		t.Fatalf("TK-1 status = %s, want complete", result.ShareStatus)
	}
	if result.AllocationClosed {
		t.Fatal("allocation should stay active while TK-2 is unfinished")
	}
	if _, ok := allocator.Active(); !ok {
		t.Fatal("allocation should still be active")
	}

	// TK-2 keeps flowing while TK-1 is already complete.
	if _, _, err := allocator.Deliver("TK-2", 20); err != nil {
		t.Fatalf("deliver TK-2: %v", err)
	}
	result, active, err = allocator.Deliver("TK-2", 20)
	if err != nil {
		t.Fatalf("finish TK-2: %v", err)
	}
	if !result.AllocationClosed || active.Phase != AllocClosed {
		t.Fatal("allocation should close when all shares complete")
	}

	// Delivering into a completed share is rejected.
	if _, _, err := allocator.Deliver("TK-1", 1); err == nil {
		t.Fatal("delivery into completed share should be rejected")
	}
}

func TestAllocatorPauseOnlyAffectsOneShare(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	active := confirmPlan(t, allocator)
	_ = active

	if err := allocator.PauseTank("TK-2", "line disconnected"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused, _ := allocator.Active()
	if shareStatus(paused, "TK-2") != SharePaused {
		t.Fatal("TK-2 share should be paused")
	}
	if shareStatus(paused, "TK-1") != ShareActive {
		t.Fatal("TK-1 share should stay active")
	}
	if _, _, err := allocator.Deliver("TK-2", 10); err == nil {
		t.Fatal("delivery to paused share should be rejected")
	}
	if _, _, err := allocator.Deliver("TK-1", 10); err != nil {
		t.Fatalf("other tank should keep accepting deliveries: %v", err)
	}

	if err := allocator.ResumeTank("TK-2"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed, _ := allocator.Active()
	if shareStatus(resumed, "TK-2") != ShareActive {
		t.Fatal("TK-2 share should be active after resume")
	}
	if _, _, err := allocator.Deliver("TK-2", 40); err != nil {
		t.Fatalf("deliver after resume: %v", err)
	}
}

func TestAllocatorDeliverRejectsOverflow(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	confirmPlan(t, allocator)

	// TK-1 share planned volume is 50.
	if _, _, err := allocator.Deliver("TK-1", 51); err == nil {
		t.Fatal("delivery over planned volume should be rejected")
	}
	// Delivery without an active share is rejected.
	if _, _, err := allocator.Deliver("TK-9", 1); err == nil {
		t.Fatal("delivery to a tank without a share should be rejected")
	}
}

func TestTankBookRejectsCapacityOverflow(t *testing.T) {
	book := NewTankBook()
	if _, err := book.Register(TankRegistration{Name: "TK", Capacity: 10, Occupied: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := book.AddOccupied("TK", 2); err == nil {
		t.Fatal("addOccupied beyond capacity should be rejected")
	}
	if tank, err := book.AddOccupied("TK", 1); err != nil {
		t.Fatalf("exact fill should be accepted: %v", err)
	} else if tank.Occupied != 10 {
		t.Fatalf("occupied = %v, want 10", tank.Occupied)
	}
}

func TestAllocatorResubmitReplacesDraft(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	plan := testPlan(90, Share{Tank: "TK-1", Planned: 50}, Share{Tank: "TK-2", Planned: 40})
	draft, err := allocator.Submit(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.ExpectedTotal = 40
	plan.Shares = []Share{{Tank: "TK-1", Planned: 40}}
	if _, err := allocator.Submit(plan); err != nil {
		t.Fatalf("resubmitting draft should replace it: %v", err)
	}
	if _, err := allocator.Confirm(draft.Campaign, draft.Revision); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := allocator.Submit(plan); err == nil {
		t.Fatal("submit after confirm should be rejected")
	}
}

func TestAllocatorCloseRevisionFreesForNextBatch(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	active := confirmPlan(t, allocator)
	if allocator.CloseRevision(active.Revision.String()) != 1 {
		t.Fatal("expected one allocation closed")
	}
	if _, ok := allocator.ForRevision(active.Revision.String()); !ok {
		t.Fatal("closed allocation must remain in history")
	}
	next := testPlan(50, Share{Tank: "TK-1", Planned: 50})
	if _, err := allocator.Submit(next); err != nil {
		t.Fatalf("next revision should be submittable: %v", err)
	}
}

func TestAllocatorFullTankCannotResumeUntilExpanded(t *testing.T) {
	book := NewTankBook()
	allocator := NewAllocator(book)
	if _, err := allocator.RegisterTanks([]TankRegistration{{Name: "TK-1", Capacity: 10, Occupied: 10}}); err != nil {
		t.Fatal(err)
	}
	revision := uuid.New()
	allocator.Restore([]model.AllocationState{{
		Campaign:      uuid.New(),
		Revision:      revision,
		Grade:         "gradeA",
		ExpectedTotal: 10,
		Phase:         string(AllocActive),
		Shares:        []model.ShareState{{Tank: "TK-1", Planned: 10, Actual: 10, Status: string(ShareFull), Reason: "tank full"}},
	}})
	if err := allocator.PauseTank("TK-1", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := allocator.ResumeTank("TK-1"); err == nil {
		t.Fatal("resume of a still-full tank should be rejected")
	}
	// Expand capacity: occupied must stay 10, share leaves headroom.
	if _, err := allocator.RegisterTanks([]TankRegistration{{Name: "TK-1", Capacity: 20, Occupied: 10}}); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if err := allocator.ResumeTank("TK-1"); err != nil {
		t.Fatalf("resume after expansion: %v", err)
	}
	restored, _ := allocator.Active()
	if shareStatus(restored, "TK-1") != ShareActive {
		t.Fatal("full share should return active once space exists")
	}
}

func TestAllocatorSnapshotRoundTrip(t *testing.T) {
	allocator, _ := newTestAllocator(t)
	active := confirmPlan(t, allocator)
	if _, _, err := allocator.Deliver("TK-1", 25); err != nil {
		t.Fatal(err)
	}
	if err := allocator.PauseTank("TK-2", "disconnected"); err != nil {
		t.Fatal(err)
	}

	tankStates := allocator.book.Snapshot()
	allocationStates := allocator.Snapshot()

	rebuiltBook := NewTankBook()
	rebuiltBook.Restore(tankStates)
	rebuilt := NewAllocator(rebuiltBook)
	rebuilt.Restore(allocationStates)

	restored, ok := rebuilt.Active()
	if !ok {
		t.Fatal("active allocation should survive restore")
	}
	if restored.Revision != active.Revision || restored.ExpectedTotal != 90 {
		t.Fatal("allocation identity/total mismatch after restore")
	}
	got := map[string]model.ShareState{}
	for _, state := range rebuilt.Snapshot()[0].Shares {
		got[state.Tank] = state
	}
	if got["TK-1"].Actual != 25 || got["TK-1"].Status != string(ShareActive) {
		t.Fatalf("TK-1 not restored: %+v", got["TK-1"])
	}
	if got["TK-2"].Status != string(SharePaused) {
		t.Fatalf("TK-2 pause not restored: %+v", got["TK-2"])
	}
	tanks := rebuiltBook.List()
	if tanks[0].Occupied != 35 { // 10 registered + 25 delivered
		t.Fatalf("TK-1 occupied = %v, want 35", tanks[0].Occupied)
	}
}
