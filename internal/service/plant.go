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
	"strings"
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
	TankBook          *product.TankBook
	ProductAllocator  *product.Allocator
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
	tankBook := product.NewTankBook()
	plant := &Plant{
		Campaign:          campaignState,
		Column:            column.New("T-101"),
		Feed:              feed.NewCoordinator(),
		Reflux:            refluxCoordinator,
		RefluxState:       reflux.New(),
		Reboiler:          reboiler.New(),
		Analyzer:          analyzerState,
		Product:           product.NewCoordinator(campaignState, analyzerState, store),
		TankBook:          tankBook,
		ProductAllocator:  product.NewAllocator(tankBook),
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
	if err := p.record("campaign.transition", grade); err != nil {
		p.fail(err)
		return model.Campaign{}, err
	}
	p.ProductAllocator.CloseRevision(before.Revision.String())
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

// routePreconditions gathers composition evidence, evaluates the route
// interlock and advances the campaign to on-spec. It is shared by the legacy
// single-target route and the multi-tank allocation flow.
func (p *Plant) routePreconditions() (model.Campaign, model.Proof, error) {
	current := p.Campaign.Snapshot()
	proof, ok := p.Analyzer.Proof(current.Revision)
	if !ok {
		return model.Campaign{}, model.Proof{}, errors.New("composition evidence is incomplete")
	}
	_ = p.Evidence.Add(proof)
	pressureProof := model.Proof{ID: uuid.New(), Revision: current.Revision, Source: "column-pressure", Digest: fmt.Sprintf("%.3f", p.Column.Snapshot().Pressure), Durable: true, Independent: true, At: time.Now().Unix()}
	permit := p.Interlock.Evaluate([]model.Proof{proof, pressureProof}, model.Campaign{ID: current.ID, Revision: current.Revision, Phase: model.OnSpec, Epoch: current.Epoch, Fencing: current.Fencing})
	if err := permit.Error(); err != nil {
		return model.Campaign{}, model.Proof{}, err
	}
	current.Phase = model.OnSpec
	campaign.Restore(p.Campaign, current)
	return current, proof, nil
}

func (p *Plant) RouteProduct(target string) error {
	current, proof, err := p.routePreconditions()
	if err != nil {
		return err
	}
	plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Target: target}
	if err := p.Product.PublishWithProof(plan, proof); err != nil {
		p.ProductAudit.Record(plan, proof, false, err.Error())
		p.fail(err)
		return err
	}
	p.ProductAudit.Record(plan, proof, true, "durable proof accepted")
	return p.record("product.routed", target)
}

// RegisterTanks records or updates receiving tank capacities and occupied
// volumes before an allocation is planned.
func (p *Plant) RegisterTanks(items []product.TankRegistration) ([]product.Tank, error) {
	tanks, err := p.ProductAllocator.RegisterTanks(items)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(tanks))
	for _, tank := range tanks {
		parts = append(parts, fmt.Sprintf("%s:%g:%g", tank.Name, tank.Capacity, tank.Occupied))
	}
	if err := p.record("tank.registered", strings.Join(parts, ",")); err != nil {
		p.fail(err)
		return nil, err
	}
	return tanks, nil
}

func (p *Plant) TankStatus() []product.Tank { return p.TankBook.List() }

// SubmitAllocation validates the per-tank planned volumes against tank
// capacity and the declared expected total, storing a draft allocation.
func (p *Plant) SubmitAllocation(expectedTotal float64, requests []product.ShareRequest) (product.Allocation, error) {
	current := p.Campaign.Snapshot()
	shares := make([]product.Share, 0, len(requests))
	for _, request := range requests {
		shares = append(shares, product.Share{Tank: request.Tank, Planned: request.Planned})
	}
	plan := product.Allocation{
		Campaign:      current.ID,
		Revision:      current.Revision,
		Grade:         current.Grade,
		ExpectedTotal: expectedTotal,
		Shares:        shares,
	}
	allocation, err := p.ProductAllocator.Submit(plan)
	if err != nil {
		return product.Allocation{}, err
	}
	parts := make([]string, 0, len(allocation.Shares))
	for _, share := range allocation.Shares {
		parts = append(parts, fmt.Sprintf("%s=%g", share.Tank, share.Planned))
	}
	payload := fmt.Sprintf("%s:%s:%g:%s", allocation.Revision, allocation.Grade, allocation.ExpectedTotal, strings.Join(parts, ","))
	if err := p.record("product.plan.submitted", payload); err != nil {
		p.fail(err)
		return product.Allocation{}, err
	}
	return allocation, nil
}

// ConfirmAllocation activates the draft allocation for the current revision
// after the same proof/interlock gate used by the legacy route passes. The
// legacy single-target route is intentionally not opened.
func (p *Plant) ConfirmAllocation() (product.Allocation, error) {
	current, proof, err := p.routePreconditions()
	if err != nil {
		p.recordAllocationFailure(p.Campaign.Snapshot(), err)
		return product.Allocation{}, err
	}
	if err := journal.PersistProof(p.Journal, proof, current); err != nil {
		p.recordAllocationFailure(current, err)
		p.fail(err)
		return product.Allocation{}, err
	}
	allocation, err := p.ProductAllocator.Confirm(current.ID, current.Revision)
	if err != nil {
		p.recordAllocationFailure(current, err)
		return product.Allocation{}, err
	}
	for _, share := range allocation.Shares {
		plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Target: share.Tank}
		p.ProductAudit.Record(plan, proof, true, "allocation share confirmed")
	}
	parts := make([]string, 0, len(allocation.Shares))
	for _, share := range allocation.Shares {
		parts = append(parts, fmt.Sprintf("%s=%g:%s", share.Tank, share.Planned, share.Status))
	}
	payload := fmt.Sprintf("%s:%s:%g:%s", allocation.Revision, allocation.Grade, allocation.ExpectedTotal, strings.Join(parts, ","))
	if err := p.record("product.allocation.confirmed", payload); err != nil {
		p.fail(err)
		return product.Allocation{}, err
	}
	return allocation, nil
}

func (p *Plant) recordAllocationFailure(current model.Campaign, cause error) {
	allocation, ok := p.ProductAllocator.ForRevision(current.Revision.String())
	if !ok {
		return
	}
	for _, share := range allocation.Shares {
		plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Target: share.Tank}
		p.ProductAudit.Record(plan, model.Proof{}, false, cause.Error())
	}
}

// RecordDelivery posts an actual inbound volume to one active share.
func (p *Plant) RecordDelivery(tank string, amount float64) (product.DeliveryResult, error) {
	result, _, err := p.ProductAllocator.Deliver(tank, amount)
	if err != nil {
		return product.DeliveryResult{}, err
	}
	payload := fmt.Sprintf("%s:%g:%g/%g:%s:%g/%g", result.Tank, result.Amount, result.Actual, result.Planned, result.ShareStatus, result.Occupied, result.Capacity)
	if err := p.record("product.delivered", payload); err != nil {
		p.fail(err)
		return product.DeliveryResult{}, err
	}
	return result, nil
}

// PauseTank disconnects one tank and pauses only its allocation share.
func (p *Plant) PauseTank(name, reason string) error {
	if err := p.ProductAllocator.PauseTank(name, reason); err != nil {
		return err
	}
	if reason == "" {
		reason = "disconnected"
	}
	if err := p.record("tank.paused", name+":"+reason); err != nil {
		p.fail(err)
		return err
	}
	return nil
}

// ResumeTank restores a disconnected tank and its pausable shares.
func (p *Plant) ResumeTank(name string) error {
	if err := p.ProductAllocator.ResumeTank(name); err != nil {
		return err
	}
	if err := p.record("tank.resumed", name); err != nil {
		p.fail(err)
		return err
	}
	return nil
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
	return model.Snapshot{Campaign: p.Campaign.Snapshot(), Column: p.Column.Snapshot(), Reflux: p.RefluxState.Snapshot(), Reboiler: p.Reboiler.Snapshot(), Products: p.ProductTargets(), Tanks: p.TankBook.Snapshot(), Allocations: p.ProductAllocator.Snapshot(), Relief: p.Relief.State.Snapshot()}
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
	p.TankBook.Restore(snapshot.Tanks)
	p.ProductAllocator.Restore(snapshot.Allocations)
	p.mu.Lock()
	p.started = snapshot.Campaign.Phase != model.Stopped
	p.mu.Unlock()
	if !model.PhaseAllowsHeat(snapshot.Campaign.Phase) {
		p.Reboiler.Stop()
	}
	return nil
}

// ProductTargets merges the legacy single target with the non-terminal tank
// shares of the active allocation, de-duplicated and order-preserving.
func (p *Plant) ProductTargets() []string {
	out := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	add := func(target string) {
		if target != "" && !seen[target] {
			seen[target] = true
			out = append(out, target)
		}
	}
	if p.Product.Route.Opened() {
		add(p.Product.Route.Target())
	}
	if allocation, ok := p.ProductAllocator.Active(); ok {
		for _, share := range allocation.Shares {
			if share.Status == product.ShareActive || share.Status == product.SharePaused || share.Status == product.ShareFull {
				add(share.Tank)
			}
		}
	}
	return out
}

func (p *Plant) Status() map[string]any {
	p.mu.RLock()
	started := p.started
	lastError := p.lastError
	p.mu.RUnlock()
	gradient, _ := p.Profile.Gradient()
	return map[string]any{"started": started, "last_error": lastError, "campaign": p.Campaign.Snapshot(), "column": p.Column.Snapshot(), "reflux_used": p.Reflux.Used(), "products": p.ProductTargets(), "tanks": p.TankStatus(), "allocation": activeAllocationView(p.ProductAllocator), "allocations": p.ProductAllocator.History(), "relief": p.Relief.State.Snapshot(), "feed_metrics": p.FeedMetrics.Snapshot(), "reflux_telemetry": p.RefluxTelemetry.Snapshot(), "reboiler_telemetry": p.ReboilerTelemetry.Snapshot(), "product_decisions": p.ProductAudit.Count(), "utility_used": p.UtilityLedger.Used(), "metrics": p.Metrics.Snapshot(), "campaign_changes": p.CampaignHistory.Count(), "profile_gradient": gradient, "profile_stable": p.Profile.Stable(40), "evidence_count": p.Evidence.Count(p.Campaign.Snapshot().Revision.String()), "relief_attempts": p.ReliefHistory.Count(), "open_incidents": p.Incidents.CountOpen(), "operational_audit": p.OperationalAudit()}
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

func activeAllocationView(allocator *product.Allocator) any {
	if allocation, ok := allocator.Active(); ok {
		return allocation
	}
	return nil
}
