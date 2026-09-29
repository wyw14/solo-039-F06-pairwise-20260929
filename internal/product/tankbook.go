package product

import (
	"errors"
	"strings"
	"sync"

	"github.com/wyw14/solo-039/internal/model"
)

const (
	TankAvailable = "available"
	TankPaused    = "paused" // manual / line-disconnected pause, unrelated to being full
)

// Tank is one receiving tank entry in the book.
type Tank struct {
	Name     string
	Capacity float64
	Occupied float64
	Status   string
}

// TankRegistration is the upsert payload for recording tank capacity and
// current occupied volume.
type TankRegistration struct {
	Name     string  `json:"name"`
	Capacity float64 `json:"capacity"`
	Occupied float64 `json:"occupied"`
}

// TankBook records receiving tanks keyed by name. It never references the
// allocator, so locking always flows Allocator -> TankBook.
type TankBook struct {
	mu    sync.RWMutex
	tanks map[string]Tank
	order []string
}

func NewTankBook() *TankBook {
	return &TankBook{tanks: make(map[string]Tank)}
}

// Register creates or updates a tank. Capacity and occupied volume are
// replaced on update, but an existing paused status is preserved.
func (b *TankBook) Register(item TankRegistration) (Tank, error) {
	name := strings.TrimSpace(item.Name)
	if err := model.ValidateTankName(name); err != nil {
		return Tank{}, err
	}
	if item.Capacity <= 0 {
		return Tank{}, errors.New("tank capacity must be positive")
	}
	if item.Occupied < 0 {
		return Tank{}, errors.New("tank occupied volume cannot be negative")
	}
	if item.Occupied > item.Capacity+model.VolumeEpsilon {
		return Tank{}, errors.New("tank occupied volume exceeds capacity")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tank, exists := b.tanks[name]
	if !exists {
		tank = Tank{Name: name, Status: TankAvailable}
		b.order = append(b.order, name)
	}
	tank.Capacity = item.Capacity
	tank.Occupied = item.Occupied
	b.tanks[name] = tank
	return tank, nil
}

func (b *TankBook) Get(name string) (Tank, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tank, ok := b.tanks[name]
	return tank, ok
}

func (b *TankBook) List() []Tank {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Tank, 0, len(b.order))
	for _, name := range b.order {
		out = append(out, b.tanks[name])
	}
	return out
}

func (b *TankBook) Pause(name string) (Tank, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tank, ok := b.tanks[name]
	if !ok {
		return Tank{}, errors.New("tank not registered")
	}
	tank.Status = TankPaused
	b.tanks[name] = tank
	return tank, nil
}

func (b *TankBook) Resume(name string) (Tank, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tank, ok := b.tanks[name]
	if !ok {
		return Tank{}, errors.New("tank not registered")
	}
	if tank.Status != TankPaused {
		return Tank{}, errors.New("tank is not paused")
	}
	tank.Status = TankAvailable
	b.tanks[name] = tank
	return tank, nil
}

// AddOccupied increases the occupied volume, rejecting overflow. Caller must
// hold the allocator lock; the book lock is taken separately.
func (b *TankBook) AddOccupied(name string, amount float64) (Tank, error) {
	if amount <= 0 {
		return Tank{}, errors.New("delivery amount must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tank, ok := b.tanks[name]
	if !ok {
		return Tank{}, errors.New("tank not registered")
	}
	if tank.Occupied+amount > tank.Capacity+model.VolumeEpsilon {
		return Tank{}, errors.New("delivery overflows tank capacity")
	}
	tank.Occupied += amount
	b.tanks[name] = tank
	return tank, nil
}

func (b *TankBook) Snapshot() []model.TankState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]model.TankState, 0, len(b.order))
	for _, name := range b.order {
		tank := b.tanks[name]
		out = append(out, model.TankState{Name: tank.Name, Capacity: tank.Capacity, Occupied: tank.Occupied, Status: tank.Status})
	}
	return out
}

func (b *TankBook) Restore(states []model.TankState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tanks = make(map[string]Tank, len(states))
	b.order = make([]string, 0, len(states))
	for _, state := range states {
		name := strings.TrimSpace(state.Name)
		if name == "" {
			continue
		}
		status := state.Status
		if status != TankAvailable && status != TankPaused {
			status = TankAvailable
		}
		tank := Tank{Name: name, Capacity: state.Capacity, Occupied: state.Occupied, Status: status}
		if _, exists := b.tanks[name]; !exists {
			b.order = append(b.order, name)
		}
		b.tanks[name] = tank
	}
}
