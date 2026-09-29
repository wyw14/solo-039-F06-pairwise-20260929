package product

import (
	"errors"

	"github.com/wyw14/solo-039/internal/model"
)

// Route 是一批合格产品的启用路线，可同时指向多个罐。旧调用方仍可通过
// Target() 访问单罐路线；多罐分配使用 Targets()/Shares()。
type Route struct {
	target   string
	targets  []string
	shares   []model.TankShare
	open     bool
	revision string
}

func (r *Route) Open(plan model.ProductPlan) error {
	if len(plan.Tanks) > 0 {
		if err := model.ValidatePlan(plan); err != nil {
			return err
		}
		r.targets = make([]string, 0, len(plan.Tanks))
		r.shares = make([]model.TankShare, 0, len(plan.Tanks))
		for _, share := range plan.Tanks {
			r.targets = append(r.targets, share.Tank)
			r.shares = append(r.shares, model.TankShare{Tank: share.Tank, Planned: share.Planned, Status: model.TankReady})
		}
		r.target = plan.Tanks[0].Tank
	} else {
		if plan.Target == "" {
			return errors.New("target required")
		}
		r.target = plan.Target
		r.targets = []string{plan.Target}
		r.shares = []model.TankShare{{Tank: plan.Target, Status: model.TankReady}}
	}
	r.revision = plan.Revision.String()
	r.open = true
	return nil
}

func (r *Route) Close() { r.open = false }

func (r *Route) Opened() bool { return r.open }

func (r *Route) Target() string { return r.target }

// Targets 返回路线上的全部罐号，顺序与提交计划一致。
func (r *Route) Targets() []string {
	out := make([]string, len(r.targets))
	copy(out, r.targets)
	return out
}

// Shares 返回路线上各罐份额的副本。
func (r *Route) Shares() []model.TankShare {
	out := make([]model.TankShare, len(r.shares))
	copy(out, r.shares)
	return out
}

// Revision 返回路线绑定的产品修订号。
func (r *Route) Revision() string { return r.revision }
