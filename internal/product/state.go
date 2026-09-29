package product

import (
	"sort"
	"sync"

	"github.com/wyw14/solo-039/internal/model"
)

// State 按罐号保存最近一次启用的路线快照，兼容单罐与多罐计划。
type State struct {
	mu     sync.RWMutex
	routes map[string]Route
}

func NewState() *State { return &State{routes: make(map[string]Route)} }

func (s *State) Put(plan model.ProductPlan, route Route) {
	s.mu.Lock()
	if len(plan.Tanks) > 0 {
		for _, share := range plan.Tanks {
			s.routes[share.Tank] = route
		}
	} else {
		s.routes[plan.Target] = route
	}
	s.mu.Unlock()
}

func (s *State) Get(target string) (Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.routes[target]
	return r, ok
}

func (s *State) Targets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.routes))
	for target := range s.routes {
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}
