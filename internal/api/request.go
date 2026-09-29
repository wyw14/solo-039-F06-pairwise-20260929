package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type transitionRequest struct {
	Grade  string `json:"grade"`
	Source string `json:"source"`
}
type sampleRequest struct {
	Value float64 `json:"value"`
	At    int64   `json:"at"`
}
type routeRequest struct {
	Target string `json:"target"`
}
type tankInput struct {
	Name     string  `json:"name"`
	Capacity float64 `json:"capacity"`
	Occupied float64 `json:"occupied"`
}
type registerTanksRequest struct {
	Tanks []tankInput `json:"tanks"`
}
type shareInput struct {
	Tank    string  `json:"tank"`
	Planned float64 `json:"planned"`
}
type allocationRequest struct {
	ExpectedTotal float64      `json:"expected_total"`
	Shares        []shareInput `json:"shares"`
}
type deliveryRequest struct {
	Tank   string  `json:"tank"`
	Amount float64 `json:"amount"`
}
type pauseTankRequest struct {
	Reason string `json:"reason"`
}
type leaseRequest struct {
	Owner  string  `json:"owner"`
	Amount float64 `json:"amount"`
}

func decodeJSON(r *http.Request, value any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return nil
}

func boolQuery(r *http.Request, key string) (bool, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New("invalid boolean query")
	}
	return value, nil
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}
