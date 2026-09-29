package product

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

type ShareStatus string

const (
	SharePlanned  ShareStatus = "planned"
	ShareActive   ShareStatus = "active"
	SharePaused   ShareStatus = "paused"   // disconnected, can be resumed
	ShareFull     ShareStatus = "full"     // tank full, blocked until emptied/expanded
	ShareComplete ShareStatus = "complete" // share planned volume fully delivered
)

type AllocationPhase string

const (
	AllocDraft  AllocationPhase = "draft"
	AllocActive AllocationPhase = "active"
	AllocClosed AllocationPhase = "closed"
)

// Share is one tank's portion of an allocation.
type Share struct {
	Tank    string
	Planned float64
	Actual  float64
	Status  ShareStatus
	Reason  string
}

// Allocation splits one qualified product batch across several tanks.
type Allocation struct {
	Campaign      uuid.UUID
	Revision      uuid.UUID
	Grade         string
	ExpectedTotal float64
	Phase         AllocationPhase
	Shares        []Share
}

// ShareRequest is one submitted tank plan entry.
type ShareRequest struct {
	Tank    string  `json:"tank"`
	Planned float64 `json:"planned"`
}

// DeliveryResult reports the outcome of recording one inbound delivery.
type DeliveryResult struct {
	Tank             string
	Amount           float64
	Actual           float64
	Planned          float64
	ShareStatus      ShareStatus
	Occupied         float64
	Capacity         float64
	AllocationClosed bool
}

// Allocator owns tank allocations and drives their lifecycle. It holds the
// only mutating entry points over TankBook, keeping lock order Allocator ->
// TankBook everywhere.
type Allocator struct {
	book  *TankBook
	mu    sync.RWMutex
	items map[string]*Allocation
	order []string
}

func NewAllocator(book *TankBook) *Allocator {
	return &Allocator{book: book, items: make(map[string]*Allocation)}
}

// RegisterTanks upserts tank registrations. Tanks referenced by the active
// allocation cannot be re-occupied out of band or shrunk below the volumes
// still promised to their shares.
func (a *Allocator) RegisterTanks(items []TankRegistration) ([]Tank, error) {
	if len(items) == 0 {
		return nil, errors.New("at least one tank is required")
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if seen[name] {
			return nil, fmt.Errorf("tank %s registered more than once in request", name)
		}
		seen[name] = true
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	active := a.activeLocked()
	// Validate everything against current state before mutating the book.
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if err := model.ValidateTankName(name); err != nil {
			return nil, err
		}
		if item.Capacity <= 0 {
			return nil, fmt.Errorf("tank %s capacity must be positive", name)
		}
		if item.Occupied < 0 {
			return nil, fmt.Errorf("tank %s occupied volume cannot be negative", name)
		}
		if item.Occupied > item.Capacity+model.VolumeEpsilon {
			return nil, fmt.Errorf("tank %s occupied volume exceeds capacity", name)
		}
		current, exists := a.book.Get(name)
		if active != nil {
			if share := shareIn(active, name); share != nil {
				if !exists {
					return nil, fmt.Errorf("tank %s is bound to the active allocation", name)
				}
				if !model.VolumeEqual(item.Occupied, current.Occupied) {
					return nil, fmt.Errorf("tank %s occupied volume can only change through deliveries", name)
				}
				required := current.Occupied
				if share.Status != ShareComplete {
					required += maxFloat(0, share.Planned-share.Actual)
				}
				if item.Capacity < required-model.VolumeEpsilon {
					return nil, fmt.Errorf("tank %s capacity cannot shrink below promised volume", name)
				}
			}
		}
	}

	out := make([]Tank, 0, len(items))
	for _, item := range items {
		tank, err := a.book.Register(item)
		if err != nil {
			return nil, err
		}
		out = append(out, tank)
	}
	return out, nil
}

// Submit stores a draft plan after checking identity, uniqueness, totals and
// per-tank remaining capacity. Resubmitting the same revision replaces its
// draft.
func (a *Allocator) Submit(plan Allocation) (Allocation, error) {
	if plan.Campaign == uuid.Nil || plan.Revision == uuid.Nil {
		return Allocation{}, errors.New("allocation identity is required")
	}
	if len(plan.Shares) == 0 {
		return Allocation{}, errors.New("at least one tank share is required")
	}
	if plan.ExpectedTotal <= 0 {
		return Allocation{}, errors.New("expected total must be positive")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	key := plan.Revision.String()
	if existing := a.items[key]; existing != nil && existing.Phase != AllocDraft {
		return Allocation{}, errors.New("allocation for this revision is already confirmed")
	}
	if a.activeLocked() != nil {
		return Allocation{}, errors.New("another allocation is already active")
	}

	total := 0.0
	seen := make(map[string]bool, len(plan.Shares))
	shares := make([]Share, 0, len(plan.Shares))
	for _, requested := range plan.Shares {
		name := strings.TrimSpace(requested.Tank)
		if err := model.ValidateTankName(name); err != nil {
			return Allocation{}, err
		}
		if seen[name] {
			return Allocation{}, fmt.Errorf("tank %s appears more than once in plan", name)
		}
		seen[name] = true
		if requested.Planned <= 0 {
			return Allocation{}, fmt.Errorf("planned volume for tank %s must be positive", name)
		}
		tank, ok := a.book.Get(name)
		if !ok {
			return Allocation{}, fmt.Errorf("tank %s is not registered", name)
		}
		if tank.Occupied+requested.Planned > tank.Capacity+model.VolumeEpsilon {
			return Allocation{}, fmt.Errorf("tank %s capacity exceeded by plan", name)
		}
		total += requested.Planned
		shares = append(shares, Share{Tank: name, Planned: requested.Planned, Status: SharePlanned})
	}
	if !model.VolumeEqual(total, plan.ExpectedTotal) {
		return Allocation{}, fmt.Errorf("planned total %g does not match expected total %g", total, plan.ExpectedTotal)
	}

	allocation := &Allocation{
		Campaign:      plan.Campaign,
		Revision:      plan.Revision,
		Grade:         plan.Grade,
		ExpectedTotal: plan.ExpectedTotal,
		Phase:         AllocDraft,
		Shares:        shares,
	}
	if a.items[key] == nil {
		a.order = append(a.order, key)
	}
	a.items[key] = allocation
	return cloneAllocation(allocation), nil
}

// Confirm activates the draft for the revision, re-checking capacity against
// the current tank book. Shares on paused tanks start paused.
func (a *Allocator) Confirm(campaignID, revision uuid.UUID) (Allocation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	allocation, ok := a.items[revision.String()]
	if !ok {
		return Allocation{}, errors.New("allocation plan not submitted")
	}
	if allocation.Phase != AllocDraft {
		return Allocation{}, errors.New("allocation is not awaiting confirmation")
	}
	if allocation.Campaign != campaignID {
		return Allocation{}, errors.New("allocation belongs to another campaign")
	}
	for i := range allocation.Shares {
		share := &allocation.Shares[i]
		tank, ok := a.book.Get(share.Tank)
		if !ok {
			return Allocation{}, fmt.Errorf("tank %s is no longer registered", share.Tank)
		}
		if tank.Occupied+share.Planned > tank.Capacity+model.VolumeEpsilon {
			return Allocation{}, fmt.Errorf("tank %s capacity no longer covers the plan", share.Tank)
		}
		if tank.Status == TankPaused {
			share.Status = SharePaused
			share.Reason = "tank paused at confirm"
		} else {
			share.Status = ShareActive
			share.Reason = ""
		}
	}
	allocation.Phase = AllocActive
	return cloneAllocation(allocation), nil
}

// Deliver records an actual inbound volume to one active share. Over-volume is
// rejected; an exact fill completes the share and a tank fill pauses only that
// share. Other shares keep flowing.
func (a *Allocator) Deliver(tank string, amount float64) (DeliveryResult, Allocation, error) {
	name := strings.TrimSpace(tank)
	if err := model.ValidateTankName(name); err != nil {
		return DeliveryResult{}, Allocation{}, err
	}
	if amount <= 0 {
		return DeliveryResult{}, Allocation{}, errors.New("delivery amount must be positive")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	allocation := a.activeLocked()
	if allocation == nil {
		return DeliveryResult{}, Allocation{}, errors.New("no active allocation")
	}
	share := shareIn(allocation, name)
	if share == nil {
		return DeliveryResult{}, Allocation{}, fmt.Errorf("tank %s has no share in the active allocation", name)
	}
	if share.Status != ShareActive {
		return DeliveryResult{}, Allocation{}, fmt.Errorf("share for tank %s is %s", name, share.Status)
	}
	current, ok := a.book.Get(name)
	if !ok {
		return DeliveryResult{}, Allocation{}, fmt.Errorf("tank %s is not registered", name)
	}
	newActual := share.Actual + amount
	if newActual > share.Planned+model.VolumeEpsilon {
		return DeliveryResult{}, Allocation{}, errors.New("delivery exceeds share planned volume")
	}
	if current.Occupied+amount > current.Capacity+model.VolumeEpsilon {
		return DeliveryResult{}, Allocation{}, errors.New("delivery overflows tank capacity")
	}

	updated, err := a.book.AddOccupied(name, amount)
	if err != nil {
		return DeliveryResult{}, Allocation{}, err
	}
	share.Actual = newActual
	switch {
	case model.VolumeEqual(newActual, share.Planned):
		share.Status = ShareComplete
		share.Reason = ""
	case model.VolumeEqual(updated.Occupied, updated.Capacity):
		share.Status = ShareFull
		share.Reason = "tank full"
	default:
		share.Status = ShareActive
	}

	closed := true
	for _, s := range allocation.Shares {
		if s.Status != ShareComplete {
			closed = false
			break
		}
	}
	if closed {
		allocation.Phase = AllocClosed
	}

	result := DeliveryResult{
		Tank:             name,
		Amount:           amount,
		Actual:           share.Actual,
		Planned:          share.Planned,
		ShareStatus:      share.Status,
		Occupied:         updated.Occupied,
		Capacity:         updated.Capacity,
		AllocationClosed: closed,
	}
	return result, cloneAllocation(allocation), nil
}

// PauseTank marks a tank disconnected and pauses its active share.
func (a *Allocator) PauseTank(name, reason string) error {
	name = strings.TrimSpace(name)
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.book.Get(name); !ok {
		return errors.New("tank not registered")
	}
	if reason == "" {
		reason = "disconnected"
	}
	if _, err := a.book.Pause(name); err != nil {
		return err
	}
	if allocation := a.activeLocked(); allocation != nil {
		if share := shareIn(allocation, name); share != nil && share.Status == ShareActive {
			share.Status = SharePaused
			share.Reason = reason
		}
	}
	return nil
}

// ResumeTank restores a disconnected tank and its pausable shares. A tank that
// is still full stays blocked until it is emptied or expanded.
func (a *Allocator) ResumeTank(name string) error {
	name = strings.TrimSpace(name)
	a.mu.Lock()
	defer a.mu.Unlock()

	tank, ok := a.book.Get(name)
	if !ok {
		return errors.New("tank not registered")
	}
	if tank.Status != TankPaused {
		return errors.New("tank is not paused")
	}
	if allocation := a.activeLocked(); allocation != nil {
		for _, s := range allocation.Shares {
			if s.Tank != name {
				continue
			}
			if s.Status == ShareFull && tank.Capacity-tank.Occupied <= model.VolumeEpsilon {
				return errors.New("tank is full; empty or expand before resume")
			}
		}
		for i := range allocation.Shares {
			share := &allocation.Shares[i]
			if share.Tank != name {
				continue
			}
			if share.Status == ShareFull || share.Status == SharePaused {
				share.Status = ShareActive
				share.Reason = ""
			}
		}
	}
	_, err := a.book.Resume(name)
	return err
}

// CloseRevision closes the active allocation of a revision, e.g. after a grade
// transition. Share history is preserved.
func (a *Allocator) CloseRevision(revision string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if allocation := a.items[revision]; allocation != nil && allocation.Phase == AllocActive {
		allocation.Phase = AllocClosed
		return 1
	}
	return 0
}

func (a *Allocator) Active() (Allocation, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if allocation := a.activeLocked(); allocation != nil {
		return cloneAllocation(allocation), true
	}
	return Allocation{}, false
}

func (a *Allocator) ForRevision(revision string) (Allocation, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if allocation := a.items[revision]; allocation != nil {
		return cloneAllocation(allocation), true
	}
	return Allocation{}, false
}

func (a *Allocator) History() []Allocation {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]Allocation, 0, len(a.order))
	for _, key := range a.order {
		out = append(out, cloneAllocation(a.items[key]))
	}
	return out
}

func (a *Allocator) Snapshot() []model.AllocationState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]model.AllocationState, 0, len(a.order))
	for _, key := range a.order {
		allocation := a.items[key]
		state := model.AllocationState{
			Campaign:      allocation.Campaign,
			Revision:      allocation.Revision,
			Grade:         allocation.Grade,
			ExpectedTotal: allocation.ExpectedTotal,
			Phase:         string(allocation.Phase),
			Shares:        make([]model.ShareState, 0, len(allocation.Shares)),
		}
		for _, share := range allocation.Shares {
			state.Shares = append(state.Shares, model.ShareState{
				Tank:    share.Tank,
				Planned: share.Planned,
				Actual:  share.Actual,
				Status:  string(share.Status),
				Reason:  share.Reason,
			})
		}
		out = append(out, state)
	}
	return out
}

func (a *Allocator) Restore(states []model.AllocationState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items = make(map[string]*Allocation, len(states))
	a.order = make([]string, 0, len(states))
	for _, state := range states {
		if state.Revision == uuid.Nil {
			continue
		}
		phase := AllocationPhase(state.Phase)
		if phase != AllocDraft && phase != AllocActive && phase != AllocClosed {
			phase = AllocClosed
		}
		shares := make([]Share, 0, len(state.Shares))
		for _, s := range state.Shares {
			status := ShareStatus(s.Status)
			switch status {
			case SharePlanned, ShareActive, SharePaused, ShareFull, ShareComplete:
			default:
				status = ShareComplete
			}
			shares = append(shares, Share{Tank: s.Tank, Planned: s.Planned, Actual: s.Actual, Status: status, Reason: s.Reason})
		}
		key := state.Revision.String()
		a.order = append(a.order, key)
		a.items[key] = &Allocation{
			Campaign:      state.Campaign,
			Revision:      state.Revision,
			Grade:         state.Grade,
			ExpectedTotal: state.ExpectedTotal,
			Phase:         phase,
			Shares:        shares,
		}
	}
}

// activeLocked returns the active allocation; caller holds a.mu.
func (a *Allocator) activeLocked() *Allocation {
	for _, key := range a.order {
		if a.items[key].Phase == AllocActive {
			return a.items[key]
		}
	}
	return nil
}

func shareIn(allocation *Allocation, tank string) *Share {
	for i := range allocation.Shares {
		if allocation.Shares[i].Tank == tank {
			return &allocation.Shares[i]
		}
	}
	return nil
}

func cloneAllocation(in *Allocation) Allocation {
	out := *in
	out.Shares = append([]Share(nil), in.Shares...)
	return out
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
