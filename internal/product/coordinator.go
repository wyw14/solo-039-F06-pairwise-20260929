package product

import (
	"errors"
	"time"

	"github.com/wyw14/solo-039/internal/analyzer"
	"github.com/wyw14/solo-039/internal/campaign"
	"github.com/wyw14/solo-039/internal/journal"
	"github.com/wyw14/solo-039/internal/model"
)

type Coordinator struct {
	Route    *Route
	Tanks    *TankBook
	Campaign *campaign.State
	Analyzer *analyzer.State
	Journal  *journal.Store
	proof    model.Proof
}

func NewCoordinator(c *campaign.State, a *analyzer.State, j *journal.Store) *Coordinator {
	return &Coordinator{Route: &Route{}, Tanks: NewTankBook(), Campaign: c, Analyzer: a, Journal: j}
}

func (c *Coordinator) Publish(plan model.ProductPlan) error {
	if plan.Campaign != c.Campaign.Snapshot().ID {
		return errors.New("campaign mismatch")
	}
	proof, ok := c.Analyzer.Proof(plan.Revision)
	if !ok {
		return errors.New("composition proof incomplete")
	}
	if err := journal.PersistProof(c.Journal, proof, c.Campaign.Snapshot()); err != nil {
		return err
	}
	c.proof = proof
	return c.Route.Open(plan)
}

func (c *Coordinator) Recover(plan model.ProductPlan) error {
	if err := journal.RequireProof(c.Journal, plan.Revision.String()); err != nil {
		return err
	}
	return c.Route.Open(plan)
}

func (c *Coordinator) PublishWithProof(plan model.ProductPlan, proof model.Proof) error {
	if plan.Campaign != c.Campaign.Snapshot().ID {
		return errors.New("campaign mismatch")
	}
	if proof.Revision != plan.Revision || proof.Digest == "" {
		return errors.New("proof does not match product revision")
	}
	if err := journal.PersistProof(c.Journal, proof, c.Campaign.Snapshot()); err != nil {
		return err
	}
	c.proof = proof
	return c.Route.Open(plan)
}

// RegisterTanks 录入各罐容量与当前占量。
func (c *Coordinator) RegisterTanks(tanks []model.Tank) error {
	return c.Tanks.Register(tanks)
}

// SubmitTankPlan 提交各罐计划量并核对合计，确认前只保存草案。
func (c *Coordinator) SubmitTankPlan(plans []model.TankPlan, batchTotal float64) (float64, error) {
	return c.Tanks.Submit(plans, batchTotal)
}

// ConfirmTankPlan 核对证据后按已提交计划启用多罐路线；每个份额都带批次身份，
// 实际入罐量可追溯到 campaign/revision。
func (c *Coordinator) ConfirmTankPlan(plans []model.TankPlan, batchTotal float64) (model.ProductRouting, error) {
	current := c.Campaign.Snapshot()
	proof, ok := c.Analyzer.Proof(current.Revision)
	if !ok {
		return model.ProductRouting{}, errors.New("composition evidence is incomplete")
	}
	// 先校验并保存草案，避免被拒计划把证据写入持久日志。
	if _, err := c.Tanks.Submit(plans, batchTotal); err != nil {
		return model.ProductRouting{}, err
	}
	if err := journal.PersistProof(c.Journal, proof, current); err != nil {
		return model.ProductRouting{}, err
	}
	c.proof = proof
	plan := model.ProductPlan{
		Campaign:   current.ID,
		Revision:   current.Revision,
		Grade:      current.Grade,
		Tanks:      plans,
		BatchTotal: batchTotal,
	}
	routing, err := c.Tanks.Confirm(plan, time.Now().Unix())
	if err != nil {
		return model.ProductRouting{}, err
	}
	// 同步旧式单路线视图，使既有状态/快照消费者看到多罐路线。
	if err := c.Route.Open(plan); err != nil {
		return model.ProductRouting{}, err
	}
	return routing, nil
}

// RestoreTanks 从快照恢复罐台账与已确认的多罐路线。
func (c *Coordinator) RestoreTanks(tanks []model.Tank, routing *model.ProductRouting) {
	c.Tanks.Restore(tanks, routing)
}

func (c *Coordinator) Proof() model.Proof { return c.proof }
