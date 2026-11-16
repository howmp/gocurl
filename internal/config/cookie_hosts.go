package config

import "sync"

type CookieHostSet struct {
	mu    sync.Mutex
	hosts []string
}

func NewCookieHostSet() *CookieHostSet {
	return &CookieHostSet{}
}

func (s *CookieHostSet) Add(raw string) {
	if s == nil || raw == "" {
		return
	}
	s.mu.Lock()
	s.hosts = append(s.hosts, raw)
	s.mu.Unlock()
}

func (s *CookieHostSet) List() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hosts...)
}
