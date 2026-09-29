package service

import (
	"context"
	"testing"

	"github.com/wyw14/solo-039/internal/model"
)

func startOnSpecPlant(t *testing.T, dir string) *Plant {
	t.Helper()
	plant := NewPlant(dir)
	if _, err := plant.Start(context.Background(), "PX", "primary"); err != nil {
		t.Fatalf("start: %v", err)
	}
	for i, at := range []int64{1, 2, 3} {
		if _, err := plant.AddSample(0.9+float64(i)*0.01, at); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	return plant
}

func TestMultiTankRoutingFlow(t *testing.T) {
	dir := t.TempDir()
	plant := startOnSpecPlant(t, dir)

	// 1) 录入各罐容量与当前占量。
	if err := plant.RegisterTanks([]model.Tank{
		{ID: "TK-1", Capacity: 100, Occupied: 50},
		{ID: "TK-2", Capacity: 80},
	}); err != nil {
		t.Fatalf("register tanks: %v", err)
	}

	// 2) 提交计划，合计与批次合格量不符时被拒。
	if _, err := plant.PlanTanks([]model.TankPlan{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}, 91); err == nil {
		t.Fatal("plan total mismatch should be rejected")
	}
	draft, err := plant.PlanTanks([]model.TankPlan{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}, 90)
	if err != nil {
		t.Fatalf("plan tanks: %v", err)
	}
	if draft["total"].(float64) != 90 {
		t.Fatalf("draft total = %v", draft["total"])
	}

	// 3) 确认后启用多罐路线，两个罐都在接收产品。
	routing, err := plant.ConfirmTankPlan([]model.TankPlan{{Tank: "TK-1", Planned: 50}, {Tank: "TK-2", Planned: 40}}, 90)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !routing.Traceable() || len(routing.Shares) != 2 {
		t.Fatalf("routing not traceable: %+v", routing)
	}
	if targets := plant.ProductTargets(); len(targets) != 2 {
		t.Fatalf("open targets = %v, want 2", targets)
	}

	// 4) 记录实际入罐量；TK-1 罐满只暂停 TK-1 份额。
	share, err := plant.RecordTankIntake("TK-1", 50)
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if share.Status != model.TankFull {
		t.Fatalf("TK-1 status = %s, want full", share.Status)
	}
	if targets := plant.ProductTargets(); len(targets) != 1 || targets[0] != "TK-2" {
		t.Fatalf("open targets after full = %v, want [TK-2]", targets)
	}

	// 5) 断线只暂停对应份额 TK-2，路线上不再有开放罐，但批次合计仍可追。
	if _, err := plant.PauseTankShare("TK-2", model.TankOffline); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if targets := plant.ProductTargets(); len(targets) != 0 {
		t.Fatalf("open targets = %v, want none", targets)
	}
	status := plant.TankRoutingStatus()
	if status["total_actual"].(float64) != 50 {
		t.Fatalf("total actual = %v, want 50", status["total_actual"])
	}

	// 6) 快照恢复后罐台账、份额状态与实际入罐量保留，批次仍可追。
	if err := plant.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	restored := NewPlant(dir)
	if err := restored.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	routing2, ok := restored.Product.Tanks.Routing()
	if !ok || !routing2.Traceable() {
		t.Fatalf("routing not restored: %+v ok=%v", routing2, ok)
	}
	if routing2.TotalActual() != 50 || routing2.Shares[0].Status != model.TankFull {
		t.Fatalf("restored routing mismatch: %+v", routing2)
	}
	if len(restored.Product.Tanks.Tanks()) != 2 {
		t.Fatal("tank ledger not restored")
	}
}
