package model

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

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
	if len(plan.Tanks) > 0 {
		seen := make(map[string]bool, len(plan.Tanks))
		for _, share := range plan.Tanks {
			if share.Tank == "" {
				return errors.New("target is required")
			}
			if seen[share.Tank] {
				return errors.New("target is duplicated")
			}
			seen[share.Tank] = true
			if share.Planned <= 0 {
				return errors.New("planned amount must be positive")
			}
		}
		return nil
	}
	if plan.Target == "" {
		return errors.New("target is required")
	}
	return nil
}

func PhaseAllowsHeat(p Phase) bool {
	return p == Warming || p == Stabilizing || p == OnSpec || p == Transitioning
}
