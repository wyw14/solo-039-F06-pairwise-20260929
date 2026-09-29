package product

import (
	"testing"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

func testPlan(plans []model.TankPlan, total float64) model.ProductPlan {
	return model.ProductPlan{Campaign: uuid.New(), Revision: uuid.New(), Grade: "PX", Tanks: plans, BatchTotal: total}
}

func TestRegisterTanksValidates(t *testing.T) {
	book := NewTankBook()
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 100, Occupied: 120}}); err == nil {
		t.Fatal("occupied over capacity should be rejected")
	}
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 0}}); err == nil {
		t.Fatal("non-positive capacity should be rejected")
	}
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 100, Occupied: 40}}); err != nil {
		t.Fatalf("valid register rejected: %v", err)
	}
	tanks := book.Tanks()
	if len(tanks) != 1 || tanks[0].Status != model.TankReady {
		t.Fatalf("unexpected tanks: %+v", tanks)
	}
}

func TestSubmitChecksTotalAndCapacity(t *testing.T) {
	book := NewTankBook()
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 100, Occupied: 20}, {ID: "TK-2", Capacity: 100, Occupied: 0}}); err != nil {
		t.Fatal(err)
	}
	plans := []model.TankPlan{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}
	if _, err := book.Submit(plans, 91); err == nil {
		t.Fatal("plan total mismatch should be rejected")
	}
	if _, err := book.Submit([]model.TankPlan{{Tank: "TK-1", Planned: 90}}, 90); err == nil {
		t.Fatal("share exceeding available capacity should be rejected")
	}
	if _, err := book.Submit([]model.TankPlan{{Tank: "UNKNOWN", Planned: 10}}, 10); err == nil {
		t.Fatal("plan for unregistered tank should be rejected")
	}
	total, err := book.Submit(plans, 90)
	if err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	if total != 90 {
		t.Fatalf("total = %v, want 90", total)
	}
}

func TestConfirmRoutingIntakeAndSharePause(t *testing.T) {
	book := NewTankBook()
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 100, Occupied: 50}, {ID: "TK-2", Capacity: 80, Occupied: 0}}); err != nil {
		t.Fatal(err)
	}
	plans := []model.TankPlan{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}
	if _, err := book.Submit(plans, 90); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(plans, 90)
	routing, err := book.Confirm(plan, 123)
	if err != nil {
		t.Fatalf("confirm rejected: %v", err)
	}
	if routing.ConfirmedAt != 123 || !routing.Traceable() {
		t.Fatalf("routing not traceable: %+v", routing)
	}
	if got := routing.OpenTargets(); len(got) != 2 {
		t.Fatalf("open targets = %v, want 2", got)
	}
	if _, err := book.Confirm(plan, 123); err == nil {
		t.Fatal("double confirm should be rejected")
	}

	// 实际入罐量累计，TK-1 恰好罐满：只暂停 TK-1 份额。
	if _, err := book.RecordIntake("TK-1", 30); err != nil {
		t.Fatal(err)
	}
	fullShare, err := book.RecordIntake("TK-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if fullShare.Status != model.TankFull {
		t.Fatalf("TK-1 status = %s, want full", fullShare.Status)
	}
	if _, err := book.RecordIntake("TK-1", 1); err == nil {
		t.Fatal("intake into full/paused share should be rejected")
	}
	// 断线只暂停 TK-2 份额；此时 TK-1 已满，路线上无开放罐。
	offShare, err := book.PauseShare("TK-2", model.TankOffline)
	if err != nil {
		t.Fatal(err)
	}
	if offShare.Status != model.TankOffline {
		t.Fatalf("TK-2 status = %s, want offline", offShare.Status)
	}
	current, _ := book.Routing()
	if got := current.OpenTargets(); len(got) != 0 {
		t.Fatalf("open targets = %v, want none", got)
	}
	if current.TotalActual() != 50 {
		t.Fatalf("total actual = %v, want 50", current.TotalActual())
	}
	// 满罐不能恢复；断线份额复装后可恢复。
	if _, err := book.ResumeShare("TK-1"); err == nil {
		t.Fatal("resume of a still-full tank should be rejected")
	}
	resumed, err := book.ResumeShare("TK-2")
	if err != nil {
		t.Fatalf("resume offline share: %v", err)
	}
	if resumed.Status != model.TankReady {
		t.Fatalf("resumed status = %s, want ready", resumed.Status)
	}
}

func TestRoutingCarriesBatchIdentity(t *testing.T) {
	book := NewTankBook()
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 100}}); err != nil {
		t.Fatal(err)
	}
	plans := []model.TankPlan{{Tank: "TK-1", Planned: 100}}
	if _, err := book.Submit(plans, 100); err != nil {
		t.Fatal(err)
	}
	plan := testPlan(plans, 100)
	routing, err := book.Confirm(plan, 1)
	if err != nil {
		t.Fatal(err)
	}
	if routing.Campaign != plan.Campaign || routing.Revision != plan.Revision {
		t.Fatal("routing lost batch identity")
	}
	if share := routing.Shares[0]; share.Tank != "TK-1" || share.Planned != 100 || share.Status != model.TankReady {
		t.Fatalf("unexpected share: %+v", share)
	}
}

func TestRestoreTanksAndRouting(t *testing.T) {
	book := NewTankBook()
	if err := book.Register([]model.Tank{{ID: "TK-1", Capacity: 100, Occupied: 60}}); err != nil {
		t.Fatal(err)
	}
	routing := model.ProductRouting{
		Campaign: uuid.New(),
		Revision: uuid.New(),
		Shares:   []model.TankShare{{Tank: "TK-1", Planned: 40, Actual: 40, Status: model.TankFull, Reason: "tank full"}},
	}
	book.Restore([]model.Tank{{ID: "TK-1", Capacity: 100, Occupied: 100, Status: model.TankFull}}, &routing)
	restored, ok := book.Routing()
	if !ok || restored.TotalActual() != 40 {
		t.Fatalf("routing not restored: %+v ok=%v", restored, ok)
	}
	book.CloseRouting()
	if _, ok := book.Routing(); ok {
		t.Fatal("routing should be closed")
	}
	if tanks := book.Tanks(); len(tanks) != 1 {
		t.Fatal("tank ledger should be retained after close for traceability")
	}
}
