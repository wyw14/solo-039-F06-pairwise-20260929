package product

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

// TankBook 持有罐容量台账、待确认的计划草案以及确认后的多罐分配路由。
// 罐满或断线只会暂停对应份额，不影响同一路线上的其他罐。
type TankBook struct {
	mu         sync.Mutex
	tanks      map[string]model.Tank
	draft      []model.TankPlan
	batchTotal float64
	routing    *model.ProductRouting
}

func NewTankBook() *TankBook {
	return &TankBook{tanks: make(map[string]model.Tank)}
}

// Register 录入或更新各罐容量与当前占量。参与活动路由的罐不允许改动台账，
// 以免与已记录的实际入罐量脱节。
func (b *TankBook) Register(tanks []model.Tank) error {
	for _, tank := range tanks {
		if err := model.ValidateTank(tank); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tank := range tanks {
		if b.routing != nil {
			if _, ok := b.findShare(tank.ID); ok {
				return errors.New("tank participates in active routing")
			}
		}
		if tank.Status == "" {
			tank.Status = model.TankReady
		}
		b.tanks[tank.ID] = tank
	}
	return nil
}

// Submit 提交各罐计划量并核对合计：batchTotal>0 时计划合计必须与之相等，
// 同时每个份额不得超过对应罐的剩余容量。确认前可反复提交覆盖草案。
func (b *TankBook) Submit(plans []model.TankPlan, batchTotal float64) (float64, error) {
	if batchTotal < 0 || math.IsNaN(batchTotal) {
		return 0, errors.New("batch total is invalid")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routing != nil {
		return 0, errors.New("routing already confirmed")
	}
	if err := model.ValidateTankPlans(plans, b.tanks, batchTotal); err != nil {
		return 0, err
	}
	total := 0.0
	for _, plan := range plans {
		total += plan.Planned
	}
	b.draft = clonePlans(plans)
	b.batchTotal = batchTotal
	return total, nil
}

// Draft 返回待确认的计划草案、批次总量及核对后的合计。
func (b *TankBook) Draft() ([]model.TankPlan, float64, float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0.0
	for _, plan := range b.draft {
		total += plan.Planned
	}
	return clonePlans(b.draft), b.batchTotal, total
}

// Confirm 按草案启用多罐路线，生成带批次身份的分配记录。
func (b *TankBook) Confirm(plan model.ProductPlan, at int64) (model.ProductRouting, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routing != nil {
		return model.ProductRouting{}, errors.New("routing already confirmed")
	}
	if len(b.draft) == 0 {
		return model.ProductRouting{}, errors.New("tank plan not submitted")
	}
	if err := model.ValidateTankPlans(b.draft, b.tanks, b.batchTotal); err != nil {
		return model.ProductRouting{}, err
	}
	if plan.Campaign == uuid.Nil {
		return model.ProductRouting{}, errors.New("plan identity is required")
	}
	shares := make([]model.TankShare, 0, len(b.draft))
	for _, planned := range b.draft {
		shares = append(shares, model.TankShare{Tank: planned.Tank, Planned: planned.Planned, Status: model.TankReady})
	}
	routing := model.ProductRouting{
		Campaign:    plan.Campaign,
		Revision:    plan.Revision,
		Grade:       plan.Grade,
		Shares:      shares,
		ConfirmedAt: at,
	}
	if at == 0 {
		routing.ConfirmedAt = time.Now().Unix()
	}
	b.routing = &routing
	b.draft = nil
	b.batchTotal = 0
	return routing, nil
}

// Routing 返回当前确认的多罐分配副本，未确认时第二个返回值为 false。
func (b *TankBook) Routing() (model.ProductRouting, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routing == nil {
		return model.ProductRouting{}, false
	}
	return *b.routing, true
}

// RecordIntake 记录某罐的实际入罐量并回写罐占量；实际累计不得超过该罐计划量。
// 罐满时该份额自动转为 full，仅暂停该罐对应的份额。
func (b *TankBook) RecordIntake(tank string, amount float64) (model.TankShare, error) {
	if math.IsNaN(amount) || amount <= 0 {
		return model.TankShare{}, errors.New("intake amount must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routing == nil {
		return model.TankShare{}, errors.New("routing not confirmed")
	}
	index, ok := b.findShare(tank)
	if !ok {
		return model.TankShare{}, errors.New("tank is not part of routing")
	}
	share := &b.routing.Shares[index]
	if share.Status != model.TankReady {
		return model.TankShare{}, errors.New("tank share is paused: " + string(share.Status))
	}
	ledger := b.tanks[tank]
	if share.Actual+amount > share.Planned+tolerance {
		return model.TankShare{}, errors.New("actual intake exceeds planned share")
	}
	if ledger.Occupied+amount > ledger.Capacity+tolerance {
		return model.TankShare{}, errors.New("intake exceeds tank capacity")
	}
	share.Actual += amount
	ledger.Occupied += amount
	if ledger.Full() {
		ledger.Status = model.TankFull
		share.Status = model.TankFull
		share.Reason = "tank full"
	}
	b.tanks[tank] = ledger
	return *share, nil
}

// PauseShare 因罐满或断线只暂停指定罐的份额，其他份额继续接收产品。
func (b *TankBook) PauseShare(tank string, reason model.TankStatus) (model.TankShare, error) {
	if !reason.IsPausable() {
		return model.TankShare{}, errors.New("pause reason must be full or offline")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routing == nil {
		return model.TankShare{}, errors.New("routing not confirmed")
	}
	index, ok := b.findShare(tank)
	if !ok {
		return model.TankShare{}, errors.New("tank is not part of routing")
	}
	share := &b.routing.Shares[index]
	share.Status = reason
	if reason == model.TankFull {
		share.Reason = "tank full"
	} else {
		share.Reason = "line disconnected"
	}
	if ledger, ok := b.tanks[tank]; ok {
		if reason == model.TankOffline {
			ledger.Status = model.TankOffline
		} else {
			ledger.Status = model.TankFull
		}
		b.tanks[tank] = ledger
	}
	return *share, nil
}

// ResumeShare 恢复被暂停的份额；罐仍满时不得恢复。
func (b *TankBook) ResumeShare(tank string) (model.TankShare, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.routing == nil {
		return model.TankShare{}, errors.New("routing not confirmed")
	}
	index, ok := b.findShare(tank)
	if !ok {
		return model.TankShare{}, errors.New("tank is not part of routing")
	}
	share := &b.routing.Shares[index]
	if share.Status == model.TankReady {
		return *share, nil
	}
	if share.Status == model.TankFull {
		if ledger, ok := b.tanks[tank]; ok && ledger.Full() {
			return model.TankShare{}, errors.New("tank is still full")
		}
	}
	share.Status = model.TankReady
	share.Reason = ""
	if ledger, ok := b.tanks[tank]; ok {
		ledger.Status = model.TankReady
		b.tanks[tank] = ledger
	}
	return *share, nil
}

// CloseRouting 在批次切换或停车时关闭当前多罐路线，保留台账供追溯。
func (b *TankBook) CloseRouting() {
	b.mu.Lock()
	b.routing = nil
	b.draft = nil
	b.batchTotal = 0
	b.mu.Unlock()
}

// Tanks 返回已录入罐台账的副本。
func (b *TankBook) Tanks() []model.Tank {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]model.Tank, 0, len(b.tanks))
	for _, tank := range b.tanks {
		out = append(out, tank)
	}
	return out
}

// Restore 从快照恢复罐台账与已确认路由。
func (b *TankBook) Restore(tanks []model.Tank, routing *model.ProductRouting) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tanks = make(map[string]model.Tank, len(tanks))
	for _, tank := range tanks {
		if tank.Status == "" {
			tank.Status = model.TankReady
		}
		b.tanks[tank.ID] = tank
	}
	b.routing = nil
	b.draft = nil
	b.batchTotal = 0
	if routing != nil {
		copyRouting := *routing
		b.routing = &copyRouting
	}
}

func (b *TankBook) findShare(tank string) (int, bool) {
	if b.routing == nil {
		return 0, false
	}
	for index := range b.routing.Shares {
		if b.routing.Shares[index].Tank == tank {
			return index, true
		}
	}
	return 0, false
}

const tolerance = 1e-9

func clonePlans(plans []model.TankPlan) []model.TankPlan {
	if len(plans) == 0 {
		return nil
	}
	out := make([]model.TankPlan, len(plans))
	copy(out, plans)
	return out
}
