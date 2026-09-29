package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-039/internal/product"
)

func (s *Server) registerTanks(w http.ResponseWriter, r *http.Request) {
	var input registerTanksRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	items := make([]product.TankRegistration, 0, len(input.Tanks))
	for _, tank := range input.Tanks {
		items = append(items, product.TankRegistration{Name: tank.Name, Capacity: tank.Capacity, Occupied: tank.Occupied})
	}
	tanks, err := s.plant.RegisterTanks(items)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"tanks": tanks})
}

func (s *Server) listTanks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tanks": s.plant.TankStatus()})
}

func (s *Server) submitAllocation(w http.ResponseWriter, r *http.Request) {
	var input allocationRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	shares := make([]product.ShareRequest, 0, len(input.Shares))
	for _, share := range input.Shares {
		shares = append(shares, product.ShareRequest{Tank: share.Tank, Planned: share.Planned})
	}
	allocation, err := s.plant.SubmitAllocation(input.ExpectedTotal, shares)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"allocation": allocation})
}

func (s *Server) confirmAllocation(w http.ResponseWriter, r *http.Request) {
	allocation, err := s.plant.ConfirmAllocation()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"allocation": allocation})
}

func (s *Server) recordDelivery(w http.ResponseWriter, r *http.Request) {
	var input deliveryRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := s.plant.RecordDelivery(input.Tank, input.Amount)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"delivery": result})
}

func (s *Server) pauseTank(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	reason := ""
	if r.ContentLength != 0 {
		var input pauseTankRequest
		if err := decodeJSON(r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		reason = input.Reason
	}
	if err := s.plant.PauseTank(name, reason); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "paused", "tank": name})
}

func (s *Server) resumeTank(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.plant.ResumeTank(name); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "resumed", "tank": name})
}
