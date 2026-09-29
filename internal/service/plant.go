package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/analyzer"
	"github.com/wyw14/solo-039/internal/campaign"
	"github.com/wyw14/solo-039/internal/column"
	"github.com/wyw14/solo-039/internal/feed"
	"github.com/wyw14/solo-039/internal/interlock"
	"github.com/wyw14/solo-039/internal/journal"
	"github.com/wyw14/solo-039/internal/model"
	"github.com/wyw14/solo-039/internal/product"
	"github.com/wyw14/solo-039/internal/reboiler"
	"github.com/wyw14/solo-039/internal/reflux"
	"github.com/wyw14/solo-039/internal/relief"
	"github.com/wyw14/solo-039/internal/utility"
	"path/filepath"
	"sync"
	"time"
)

type Plant struct {
	mu                sync.RWMutex
	Campaign          *campaign.State
	Column            *column.State
	Feed              *feed.Coordinator
	Reflux            *reflux.Coordinator
	RefluxState       *reflux.State
	Reboiler          *reboiler.State
	Analyzer          *analyzer.State
	Product           *product.Coordinator
	Interlock         *interlock.Coordinator
	Relief            *relief.Coordinator
	Capacity          *utility.Capacity
	Leases            *utility.LeaseBook
	Journal           *journal.Store
	FileLog           *journal.FileLog
	Snapshots         *journal.SnapshotFile
	FeedMetrics       *feed.Metrics
	RefluxTelemetry   *reflux.Telemetry
	ReboilerTelemetry *reboiler.Telemetry
	ProductAudit      *product.Audit
	UtilityLedger     *utility.Ledger
	Metrics           *Metrics
	CampaignHistory   *campaign.History
	Profile           *column.Profile
	RefluxAllocator   *reflux.Allocator
	ReboilerCommands  *reboiler.CommandBook
	Evidence          *analyzer.EvidenceCatalog
	ReliefHistory     *relief.History
	Incidents         *Incidents
	started           bool
	lastError         string
}

func NewPlant(dataDir string) *Plant {
	campaignState := campaign.New("baseline")
	analyzerState := analyzer.New()
	store := journal.New()
	refluxCoordinator := reflux.NewCoordinator(1)
	plant := &Plant{
		Campaign:          campaignState,
		Column:            column.New("T-101"),
		Feed:              feed.NewCoordinator(),
		Reflux:            refluxCoordinator,
		RefluxState:       reflux.New(),
		Reboiler:          reboiler.New(),
		Analyzer:          analyzerState,
		Product:           product.NewCoordinator(campaignState, analyzerState, store),
		Interlock:         interlock.NewCoordinator(),
		Relief:            relief.NewCoordinator(),
		Capacity:          utility.NewCapacity(1),
		Leases:            utility.NewLeaseBook(),
		Journal:           store,
		FileLog:           journal.NewFileLog(filepath.Join(dataDir, "events.jsonl")),
		Snapshots:         journal.NewSnapshotFile(filepath.Join(dataDir, "snapshot.json")),
		FeedMetrics:       feed.NewMetrics(),
		RefluxTelemetry:   reflux.NewTelemetry(),
		ReboilerTelemetry: reboiler.NewTelemetry(),
		ProductAudit:      product.NewAudit(),
		UtilityLedger:     utility.NewLedger(),
		Metrics:           NewMetrics(),
		CampaignHistory:   campaign.NewHistory(),
		Profile:           column.NewProfile(),
		RefluxAllocator:   reflux.NewAllocator(refluxCoordinator),
		ReboilerCommands:  reboiler.NewCommandBook(),
		Evidence:          analyzer.NewEvidenceCatalog(),
		ReliefHistory:     relief.NewHistory(),
		Incidents:         NewIncidents(),
	}
	plant.Reflux.Register("T-101", plant.RefluxState)
	plant.Feed.Observe = plant.observeFeed
	return plant
}

func (p *Plant) Start(ctx context.Context, grade, source string) (model.Campaign, error) {
	var operationErr error
	defer func() { p.Metrics.Record("start", operationErr) }()
	if err := model.ValidateGrade(grade); err != nil {
		operationErr = err
		return model.Campaign{}, err
	}
	p.mu.Lock()
	if p.started {
		current := p.Campaign.Snapshot()
		p.mu.Unlock()
		return current, errors.New("plant already started")
	}
	p.started = true
	p.lastError = ""
	p.mu.Unlock()
	current := p.Campaign.Snapshot()
	before := current
	current.Grade = grade
	campaign.Restore(p.Campaign, current)
	p.CampaignHistory.Record(before, current, "start grade selected")
	if err := p.Feed.Switch(ctx, source, current); err != nil {
		operationErr = err
		p.fail(err)
		return model.Campaign{}, err
	}
	p.Column.SetFeedRevision(p.Feed.Current.Revision)
	p.Analyzer.BindFeed(p.Feed.Current.Revision)
	p.Campaign.Advance(model.Warming)
	if err := p.Reboiler.Ramp(ctx, p.Campaign.Snapshot(), 0.5); err != nil {
		operationErr = err
		p.fail(err)
		return model.Campaign{}, err
	}
	p.Campaign.Advance(model.Stabilizing)
	p.Column.Apply(p.Campaign.Snapshot(), 1.1, 0.5)
	_ = p.Profile.Put(column.TrayReading{Tray: 1, Temperature: 118, Pressure: 1.1})
	_ = p.Profile.Put(column.TrayReading{Tray: 20, Temperature: 82, Pressure: 0.9})
	p.ReboilerTelemetry.Record(p.Reboiler.Snapshot())
	if err := p.record("plant.started", source); err != nil {
		p.fail(err)
		operationErr = err
		return model.Campaign{}, err
	}
	return p.Campaign.Snapshot(), nil
}

func (p *Plant) Transition(ctx context.Context, grade, source string) (model.Campaign, error) {
	if err := model.ValidateGrade(grade); err != nil {
		return model.Campaign{}, err
	}
	before := p.Campaign.Snapshot()
	current := p.Campaign.Advance(model.Transitioning)
	current.Grade = grade
	current.Revision = uuid.New()
	campaign.Restore(p.Campaign, current)
	p.CampaignHistory.Record(before, current, "grade transition")
	if err := p.Feed.Switch(ctx, source, current); err != nil {
		p.fail(err)
		return model.Campaign{}, err
	}
	p.Column.SetFeedRevision(p.Feed.Current.Revision)
	p.FeedMetrics.Record(source)
	p.Analyzer.BindFeed(p.Feed.Current.Revision)
	// 切换批次：关闭上一批的多罐路线，罐台账与事件保留供追溯。
	p.Product.Tanks.CloseRouting()
	if err := p.record("campaign.transition", grade); err != nil {
		p.fail(err)
		return model.Campaign{}, err
	}
	return p.Campaign.Snapshot(), nil
}

func (p *Plant) AddSample(value float64, at int64) (int, error) {
	revision := p.Feed.Current.Revision
	calibration := p.Analyzer.Calibration()
	ok := p.Analyzer.Add(model.Sample{ID: uuid.New(), FeedRevision: revision, Calibration: calibration, At: at, Component: "top", Value: value, Valid: true})
	if !ok {
		return p.Analyzer.SampleCount(), errors.New("sample rejected by current evidence generation")
	}
	return p.Analyzer.SampleCount(), nil
}

func (p *Plant) CalibrateAnalyzer() uuid.UUID {
	revision := p.Analyzer.Calibrate()
	p.Analyzer.BindFeed(p.Feed.Current.Revision)
	return revision
}

func (p *Plant) RouteProduct(target string) error {
	current := p.Campaign.Snapshot()
	proof, err := p.requireRoutePermit(current)
	if err != nil {
		return err
	}
	current.Phase = model.OnSpec
	campaign.Restore(p.Campaign, current)
	plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Target: target}
	if err := p.Product.PublishWithProof(plan, proof); err != nil {
		p.ProductAudit.Record(plan, proof, false, err.Error())
		p.fail(err)
		return err
	}
	p.ProductAudit.Record(plan, proof, true, "durable proof accepted")
	return p.record("product.routed", target)
}

// requireRoutePermit 在进入合格产品外送阶段前核对成分证据、塔压证据与联锁许可。
func (p *Plant) requireRoutePermit(current model.Campaign) (model.Proof, error) {
	proof, ok := p.Analyzer.Proof(current.Revision)
	if !ok {
		return model.Proof{}, errors.New("composition evidence is incomplete")
	}
	_ = p.Evidence.Add(proof)
	pressureProof := model.Proof{ID: uuid.New(), Revision: current.Revision, Source: "column-pressure", Digest: fmt.Sprintf("%.3f", p.Column.Snapshot().Pressure), Durable: true, Independent: true, At: time.Now().Unix()}
	permit := p.Interlock.Evaluate([]model.Proof{proof, pressureProof}, model.Campaign{ID: current.ID, Revision: current.Revision, Phase: model.OnSpec, Epoch: current.Epoch, Fencing: current.Fencing})
	if err := permit.Error(); err != nil {
		return model.Proof{}, err
	}
	return proof, nil
}

// RegisterTanks 录入各罐容量与当前占量，台账随快照持久化。
func (p *Plant) RegisterTanks(tanks []model.Tank) error {
	if err := p.Product.RegisterTanks(tanks); err != nil {
		return err
	}
	payload, err := encodeEventPayload(map[string]any{"tanks": p.Product.Tanks.Tanks()})
	if err != nil {
		return err
	}
	return p.record("tank.registered", payload)
}

// PlanTanks 提交各罐计划量并核对合计；确认前只保存草案，可反复提交。
func (p *Plant) PlanTanks(plans []model.TankPlan, batchTotal float64) (map[string]any, error) {
	total, err := p.Product.SubmitTankPlan(plans, batchTotal)
	if err != nil {
		return nil, err
	}
	draft, submittedTotal, _ := p.Product.Tanks.Draft()
	payload, err := encodeEventPayload(map[string]any{"batch_total": batchTotal, "total": total, "plans": plans})
	if err != nil {
		return nil, err
	}
	if err := p.record("tank.plan_submitted", payload); err != nil {
		p.fail(err)
		return nil, err
	}
	return map[string]any{"batch_total": submittedTotal, "total": total, "plans": draft}, nil
}

// ConfirmTankPlan 再次核对计划合计后按计划启用多罐路线；事件携带批次身份，
// 各份额实际入罐量均可追溯到 campaign/revision。
func (p *Plant) ConfirmTankPlan(plans []model.TankPlan, batchTotal float64) (model.ProductRouting, error) {
	current := p.Campaign.Snapshot()
	proof, err := p.requireRoutePermit(current)
	if err != nil {
		return model.ProductRouting{}, err
	}
	routing, err := p.Product.ConfirmTankPlan(plans, batchTotal)
	if err != nil {
		plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Tanks: plans, BatchTotal: batchTotal}
		p.ProductAudit.Record(plan, proof, false, err.Error())
		p.fail(err)
		return model.ProductRouting{}, err
	}
	current.Phase = model.OnSpec
	campaign.Restore(p.Campaign, current)
	plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Tanks: plans, BatchTotal: batchTotal}
	p.ProductAudit.Record(plan, proof, true, "multi-tank routing confirmed")
	payload, err := encodeEventPayload(routing)
	if err != nil {
		p.fail(err)
		return model.ProductRouting{}, err
	}
	if err := p.record("tank.plan_confirmed", payload); err != nil {
		p.fail(err)
		return model.ProductRouting{}, err
	}
	return routing, nil
}

// RecordTankIntake 记录某罐实际入罐量；罐满时只暂停该罐份额。
func (p *Plant) RecordTankIntake(tank string, amount float64) (model.TankShare, error) {
	share, err := p.Product.Tanks.RecordIntake(tank, amount)
	if err != nil {
		return model.TankShare{}, err
	}
	payload, err := encodeEventPayload(map[string]any{"tank": tank, "amount": amount, "share": share})
	if err != nil {
		return model.TankShare{}, err
	}
	if err := p.record("tank.intake", payload); err != nil {
		p.fail(err)
		return model.TankShare{}, err
	}
	return share, nil
}

// PauseTankShare 因罐满或断线只暂停对应罐的份额，其他份额继续接收产品。
func (p *Plant) PauseTankShare(tank string, reason model.TankStatus) (model.TankShare, error) {
	share, err := p.Product.Tanks.PauseShare(tank, reason)
	if err != nil {
		return model.TankShare{}, err
	}
	payload, err := encodeEventPayload(map[string]any{"tank": tank, "reason": reason, "share": share})
	if err != nil {
		return model.TankShare{}, err
	}
	if err := p.record("tank.share_paused", payload); err != nil {
		p.fail(err)
		return model.TankShare{}, err
	}
	return share, nil
}

// ResumeTankShare 恢复被暂停的份额（断线复装且罐未满）。
func (p *Plant) ResumeTankShare(tank string) (model.TankShare, error) {
	share, err := p.Product.Tanks.ResumeShare(tank)
	if err != nil {
		return model.TankShare{}, err
	}
	payload, err := encodeEventPayload(map[string]any{"tank": tank, "share": share})
	if err != nil {
		return model.TankShare{}, err
	}
	if err := p.record("tank.share_resumed", payload); err != nil {
		p.fail(err)
		return model.TankShare{}, err
	}
	return share, nil
}

// TankRoutingStatus 返回罐台账、待确认草案与当前多罐路线。
func (p *Plant) TankRoutingStatus() map[string]any {
	draft, batchTotal, total := p.Product.Tanks.Draft()
	routing, confirmed := p.Product.Tanks.Routing()
	status := map[string]any{
		"tanks":       p.Product.Tanks.Tanks(),
		"draft":       draft,
		"batch_total": batchTotal,
		"draft_total": total,
		"confirmed":   confirmed,
	}
	if confirmed {
		status["routing"] = routing
		status["open_targets"] = routing.OpenTargets()
		status["total_planned"] = routing.TotalPlanned()
		status["total_actual"] = routing.TotalActual()
	}
	return status
}

func (p *Plant) AcquireReflux(owner string, amount float64) (model.Lease, error) {
	current := p.Campaign.Snapshot()
	lease, err := p.Reflux.Acquire(owner, amount, current.Fencing)
	if err != nil {
		p.Incidents.Open("reflux-capacity", err.Error(), current.Revision.String())
		return model.Lease{}, err
	}
	p.Leases.Put(lease)
	p.UtilityLedger.Commit(lease)
	p.RefluxTelemetry.Record(p.RefluxState.Snapshot())
	if err := p.record("reflux.lease", owner); err != nil {
		p.Reflux.Release(lease)
		return model.Lease{}, err
	}
	return lease, nil
}

func (p *Plant) Shutdown(ctx context.Context, reliefTimeout bool) (relief.ShutdownResult, error) {
	current := p.Campaign.Advance(model.Shutting)
	reliefErr := p.Relief.Shutdown(ctx, current, "flare", reliefTimeout)
	steamErr := p.ReboilerCommands.Close(ctx, p.Reboiler, current)
	p.ReboilerTelemetry.Record(p.Reboiler.Snapshot())
	result := relief.CombineShutdown(reliefErr, steamErr, p.Relief.State.Snapshot(), p.Reboiler.ClosureConfirmed(current.Epoch))
	p.ReliefHistory.Record(p.Relief.State.Snapshot(), reliefErr)
	if err := result.Error(); err != nil {
		p.Incidents.Open("shutdown", err.Error(), current.Revision.String())
		p.fail(err)
		return result, err
	}
	p.Campaign.Stop()
	p.mu.Lock()
	p.started = false
	p.mu.Unlock()
	return result, p.record("plant.stopped", "safe")
}

func (p *Plant) Snapshot() model.Snapshot {
	snapshot := model.Snapshot{Campaign: p.Campaign.Snapshot(), Column: p.Column.Snapshot(), Reflux: p.RefluxState.Snapshot(), Reboiler: p.Reboiler.Snapshot(), Products: p.ProductTargets(), Relief: p.Relief.State.Snapshot(), Tanks: p.Product.Tanks.Tanks()}
	if routing, ok := p.Product.Tanks.Routing(); ok {
		snapshot.Routing = &routing
	}
	return snapshot
}

func (p *Plant) Save() error { return p.Snapshots.Save(p.Snapshot()) }

func (p *Plant) Restore() error {
	snapshot, err := p.Snapshots.Load()
	if err != nil {
		return err
	}
	campaign.Restore(p.Campaign, snapshot.Campaign)
	column.Restore(p.Column, snapshot.Column)
	if err := reboiler.Restore(p.Reboiler, snapshot.Reboiler, snapshot.Campaign); err != nil {
		return err
	}
	if err := journal.ValidateRecovery(snapshot, p.Journal.Events()); err != nil {
		return err
	}
	p.Product.RestoreTanks(snapshot.Tanks, snapshot.Routing)
	p.mu.Lock()
	p.started = snapshot.Campaign.Phase != model.Stopped
	p.mu.Unlock()
	if !model.PhaseAllowsHeat(snapshot.Campaign.Phase) {
		p.Reboiler.Stop()
	}
	return nil
}

func (p *Plant) ProductTargets() []string {
	if routing, ok := p.Product.Tanks.Routing(); ok {
		return routing.OpenTargets()
	}
	if p.Product.Route.Opened() {
		return []string{p.Product.Route.Target()}
	}
	return []string{}
}

func (p *Plant) Status() map[string]any {
	p.mu.RLock()
	started := p.started
	lastError := p.lastError
	p.mu.RUnlock()
	gradient, _ := p.Profile.Gradient()
	return map[string]any{"started": started, "last_error": lastError, "campaign": p.Campaign.Snapshot(), "column": p.Column.Snapshot(), "reflux_used": p.Reflux.Used(), "products": p.ProductTargets(), "tank_routing": p.TankRoutingStatus(), "relief": p.Relief.State.Snapshot(), "feed_metrics": p.FeedMetrics.Snapshot(), "reflux_telemetry": p.RefluxTelemetry.Snapshot(), "reboiler_telemetry": p.ReboilerTelemetry.Snapshot(), "product_decisions": p.ProductAudit.Count(), "utility_used": p.UtilityLedger.Used(), "metrics": p.Metrics.Snapshot(), "campaign_changes": p.CampaignHistory.Count(), "profile_gradient": gradient, "profile_stable": p.Profile.Stable(40), "evidence_count": p.Evidence.Count(p.Campaign.Snapshot().Revision.String()), "relief_attempts": p.ReliefHistory.Count(), "open_incidents": p.Incidents.CountOpen(), "operational_audit": p.OperationalAudit()}
}

func (p *Plant) record(kind, payload string) error {
	event := model.NewEvent(kind, p.Campaign.Snapshot(), payload)
	event.At = time.Now().UnixNano()
	if err := p.Journal.Append(event); err != nil {
		return err
	}
	if err := p.FileLog.Append(event); err != nil {
		return err
	}
	return p.Save()
}

func (p *Plant) observeFeed(value feed.Transition) {
	payload := fmt.Sprintf("%s:%s:%t:%.2f:%s:%t", value.Stage, value.OldSource, value.OldOpen, value.OldFlow, value.NewSource, value.NewOpen)
	if err := p.record("feed.transition", payload); err != nil {
		p.fail(err)
	}
	if value.Stage == "new-active" {
		p.FeedMetrics.Record(value.NewSource)
	}
}

func (p *Plant) fail(err error) {
	p.mu.Lock()
	p.lastError = err.Error()
	p.mu.Unlock()
	p.Incidents.Open("runtime", err.Error(), p.Campaign.Snapshot().Revision.String())
}
