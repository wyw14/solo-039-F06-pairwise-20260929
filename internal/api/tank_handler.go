package api

import (
	"errors"
	"net/http"

	"github.com/wyw14/solo-039/internal/model"
)

func (s *Server) registerTanks(w http.ResponseWriter, r *http.Request) {
	var input tanksRegisterRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(input.Tanks) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tanks are required"})
		return
	}
	if err := s.plant.RegisterTanks(input.Tanks); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"tanks": s.plant.TankRoutingStatus()["tanks"]})
}

func (s *Server) planTanks(w http.ResponseWriter, r *http.Request) {
	var input tankPlanRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(input.Plans) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plans are required"})
		return
	}
	result, err := s.plant.PlanTanks(input.Plans, input.BatchTotal)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) confirmTankPlan(w http.ResponseWriter, r *http.Request) {
	var input tankPlanRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(input.Plans) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plans are required"})
		return
	}
	routing, err := s.plant.ConfirmTankPlan(input.Plans, input.BatchTotal)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, routing)
}

func (s *Server) tankIntake(w http.ResponseWriter, r *http.Request) {
	var input tankIntakeRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	share, err := s.plant.RecordTankIntake(input.Tank, input.Amount)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, share)
}

func (s *Server) pauseTankShare(w http.ResponseWriter, r *http.Request) {
	var input tankShareRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	reason, err := parsePauseReason(input.Reason)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	share, err := s.plant.PauseTankShare(input.Tank, reason)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, share)
}

func (s *Server) resumeTankShare(w http.ResponseWriter, r *http.Request) {
	var input tankShareRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	share, err := s.plant.ResumeTankShare(input.Tank)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, share)
}

func (s *Server) tankRouting(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.plant.TankRoutingStatus())
}

func parsePauseReason(raw string) (model.TankStatus, error) {
	switch model.TankStatus(raw) {
	case model.TankFull:
		return model.TankFull, nil
	case model.TankOffline:
		return model.TankOffline, nil
	default:
		return "", errors.New(`reason must be "full" or "offline"`)
	}
}
