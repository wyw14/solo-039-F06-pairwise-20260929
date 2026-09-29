package model

import (
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
)

// VolumeEpsilon is the relative tolerance used when comparing tank volumes.
const VolumeEpsilon = 1e-6

// VolumeEqual compares volumes with a mixed absolute/relative tolerance so the
// check stays meaningful across different volume magnitudes.
func VolumeEqual(a, b float64) bool {
	scale := math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
	return math.Abs(a-b) <= VolumeEpsilon*scale
}

func ValidateGrade(grade string) error {
	grade = strings.TrimSpace(grade)
	if grade == "" {
		return errors.New("grade is required")
	}
	if len(grade) > 32 {
		return errors.New("grade is too long")
	}
	return nil
}

func ValidatePlan(plan ProductPlan) error {
	if plan.Campaign == uuid.Nil || plan.Revision == uuid.Nil {
		return errors.New("plan identity is required")
	}
	if plan.Target == "" {
		return errors.New("target is required")
	}
	return nil
}

// ValidateTankName ensures a tank identifier is present and bounded in length.
func ValidateTankName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("tank name is required")
	}
	if len(name) > 64 {
		return errors.New("tank name is too long")
	}
	return nil
}

func PhaseAllowsHeat(p Phase) bool {
	return p == Warming || p == Stabilizing || p == OnSpec || p == Transitioning
}
