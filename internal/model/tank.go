package model

import (
	"errors"
	"math"

	"github.com/google/uuid"
)

// TankStatus 记录单个罐份额当前是否可接收产品。
type TankStatus string

const (
	TankReady   TankStatus = "ready"
	TankPaused  TankStatus = "paused"
	TankFull    TankStatus = "full"
	TankOffline TankStatus = "offline"
)

// PausableTankStatuses 是可由“罐满/断线”触发的按份额暂停原因。
var pausableReasons = map[TankStatus]bool{TankFull: true, TankOffline: true}

// IsPausable 判断状态是否属于“只暂停对应份额”的罐满或断线。
func (s TankStatus) IsPausable() bool { return pausableReasons[s] }

func (s TankStatus) Valid() bool {
	switch s {
	case TankReady, TankPaused, TankFull, TankOffline:
		return true
	}
	return false
}

// Tank 是一个目标罐的容量台账：容量与当前占量在分配前录入。
type Tank struct {
	ID       string     `json:"id"`
	Capacity float64    `json:"capacity"`
	Occupied float64    `json:"occupied"`
	Status   TankStatus `json:"status"`
}

// Available 返回罐内尚可接收的量。
func (t Tank) Available() float64 { return t.Capacity - t.Occupied }

// Full 在占量达到容量时成立。
func (t Tank) Full() bool { return t.Occupied >= t.Capacity }

// TankPlan 是提交核对阶段中单个罐的计划份额。
type TankPlan struct {
	Tank    string  `json:"tank"`
	Planned float64 `json:"planned"`
}

// TankShare 是确认启用后路线上单个罐的份额，携带实际入罐量。
type TankShare struct {
	Tank    string     `json:"tank"`
	Planned float64    `json:"planned"`
	Actual  float64    `json:"actual"`
	Status  TankStatus `json:"status"`
	Reason  string     `json:"reason,omitempty"`
}

// ProductRouting 是一批合格产品的多罐分配。Campaign/Revision 使每个份额
// 的实际入罐量都能回溯到批次来源。
type ProductRouting struct {
	Campaign    uuid.UUID   `json:"campaign"`
	Revision    uuid.UUID   `json:"revision"`
	Grade       string      `json:"grade"`
	Shares      []TankShare `json:"shares"`
	ConfirmedAt int64       `json:"confirmed_at"`
}

// TotalPlanned 汇总各罐计划量。
func (r ProductRouting) TotalPlanned() float64 {
	var total float64
	for _, share := range r.Shares {
		total += share.Planned
	}
	return total
}

// TotalActual 汇总各罐实际入罐量。
func (r ProductRouting) TotalActual() float64 {
	var total float64
	for _, share := range r.Shares {
		total += share.Actual
	}
	return total
}

// OpenTargets 返回仍在接收产品的罐号（罐满/断线的份额被排除）。
func (r ProductRouting) OpenTargets() []string {
	out := make([]string, 0, len(r.Shares))
	for _, share := range r.Shares {
		if share.Status == TankReady {
			out = append(out, share.Tank)
		}
	}
	return out
}

// Targets 返回全部参与分配的罐号，含已暂停份额。
func (r ProductRouting) Targets() []string {
	out := make([]string, 0, len(r.Shares))
	for _, share := range r.Shares {
		out = append(out, share.Tank)
	}
	return out
}

// Traceable 在批次身份完整时成立，保证实际入罐量可回溯批次来源。
func (r ProductRouting) Traceable() bool {
	return r.Campaign != uuid.Nil && r.Revision != uuid.Nil && len(r.Shares) > 0
}

const tankTolerance = 1e-9

func amountsEqual(a, b float64) bool { return math.Abs(a-b) <= tankTolerance }

// ValidateTank 校验录入的罐容量与当前占量。
func ValidateTank(tank Tank) error {
	if tank.ID == "" {
		return errors.New("tank id is required")
	}
	if math.IsNaN(tank.Capacity) || math.IsNaN(tank.Occupied) {
		return errors.New("tank amount is not numeric")
	}
	if tank.Capacity <= 0 {
		return errors.New("tank capacity must be positive")
	}
	if tank.Occupied < 0 {
		return errors.New("tank occupied must not be negative")
	}
	if tank.Occupied > tank.Capacity+tankTolerance {
		return errors.New("tank occupied exceeds capacity")
	}
	if tank.Status != "" && !tank.Status.Valid() {
		return errors.New("tank status is invalid")
	}
	return nil
}

// ValidateTankPlans 校验各罐计划量：罐号存在且不重复、计划量为正、不超过
// 罐的剩余容量；batchTotal 为正时还要求计划合计与批次合格量一致。
func ValidateTankPlans(plans []TankPlan, tanks map[string]Tank, batchTotal float64) error {
	if len(plans) == 0 {
		return errors.New("at least one tank plan is required")
	}
	seen := make(map[string]bool, len(plans))
	var total float64
	for _, plan := range plans {
		if plan.Tank == "" {
			return errors.New("tank id is required")
		}
		if seen[plan.Tank] {
			return errors.New("tank plan is duplicated")
		}
		seen[plan.Tank] = true
		if math.IsNaN(plan.Planned) || plan.Planned <= 0 {
			return errors.New("planned amount must be positive")
		}
		tank, ok := tanks[plan.Tank]
		if !ok {
			return errors.New("tank is not registered")
		}
		if tank.Status == TankOffline {
			return errors.New("tank is offline")
		}
		if plan.Planned > tank.Available()+tankTolerance {
			return errors.New("planned amount exceeds tank available capacity")
		}
		total += plan.Planned
	}
	if batchTotal > 0 && !amountsEqual(total, batchTotal) {
		return errors.New("planned total does not match batch total")
	}
	return nil
}
