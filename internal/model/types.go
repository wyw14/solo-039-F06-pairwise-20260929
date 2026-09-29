package model

import "github.com/google/uuid"

type Phase string

const (
	Preparing     Phase = "preparing"
	Warming       Phase = "warming"
	Stabilizing   Phase = "stabilizing"
	OnSpec        Phase = "on-spec"
	Transitioning Phase = "transitioning"
	Shutting      Phase = "shutting"
	Stopped       Phase = "stopped"
)

type Campaign struct {
	ID       uuid.UUID
	Grade    string
	Revision uuid.UUID
	Phase    Phase
	Epoch    uint64
	Pressure float64
	Level    float64
	Fencing  uint64
}

type Sample struct {
	ID           uuid.UUID
	FeedRevision uuid.UUID
	Calibration  uuid.UUID
	At           int64
	Component    string
	Value        float64
	Valid        bool
}

type Proof struct {
	ID          uuid.UUID
	Revision    uuid.UUID
	Source      string
	Digest      string
	Durable     bool
	Independent bool
	At          int64
}

type ProductPlan struct {
	Campaign uuid.UUID
	Revision uuid.UUID
	Grade    string
	Target   string
}

// TankState is the persisted view of one receiving tank in the tank book.
type TankState struct {
	Name     string  `json:"name"`
	Capacity float64 `json:"capacity"`
	Occupied float64 `json:"occupied"`
	Status   string  `json:"status"` // available | paused
}

// ShareState is one tank share within a persisted allocation.
type ShareState struct {
	Tank    string  `json:"tank"`
	Planned float64 `json:"planned"`
	Actual  float64 `json:"actual"`
	Status  string  `json:"status"` // planned | active | paused | full | complete
	Reason  string  `json:"reason,omitempty"`
}

// AllocationState is the persisted multi-tank allocation for one revision.
type AllocationState struct {
	Campaign      uuid.UUID    `json:"campaign"`
	Revision      uuid.UUID    `json:"revision"`
	Grade         string       `json:"grade"`
	ExpectedTotal float64      `json:"expected_total"`
	Phase         string       `json:"phase"` // draft | active | closed
	Shares        []ShareState `json:"shares"`
}

type Lease struct {
	ID       uuid.UUID
	Owner    string
	Capacity float64
	Fence    uint64
	Active   bool
}

type Event struct {
	ID       uuid.UUID
	Kind     string
	Campaign uuid.UUID
	Revision uuid.UUID
	Epoch    uint64
	Payload  string
	At       int64
}

type Snapshot struct {
	Campaign    Campaign
	Column      Column
	Reflux      Reflux
	Reboiler    Reboiler
	Products    []string
	Tanks       []TankState
	Allocations []AllocationState
	Relief      Relief
}

type Column struct {
	ID           string
	Pressure     float64
	Temperature  float64
	Level        float64
	FeedRevision uuid.UUID
	Phase        Phase
	Fencing      uint64
}

type Reflux struct {
	Ratio        float64
	PumpCapacity float64
	Lease        Lease
	CommandEpoch uint64
}

type Reboiler struct {
	Steam        float64
	Closed       bool
	CommandEpoch uint64
	Trip         uint64
}

type Relief struct {
	Valve    string
	Pressure float64
	TimedOut bool
	Released bool
}
