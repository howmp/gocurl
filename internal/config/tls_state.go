package config

import (
	"crypto/x509"
	"strings"
	"sync"
)

type TLSState struct {
	Version          uint16
	CipherSuite      uint16
	ALPN             string
	Verified         bool
	PeerCertificates []*x509.Certificate
}

type TLSStateStore struct {
	mu     sync.RWMutex
	states map[string]TLSState
}

func NewTLSStateStore() *TLSStateStore {
	return &TLSStateStore{states: make(map[string]TLSState)}
}

func (s *TLSStateStore) Set(host string, state TLSState) {
	if s == nil {
		return
	}
	host = strings.ToLower(host)
	s.mu.Lock()
	if s.states == nil {
		s.states = make(map[string]TLSState)
	}
	s.states[host] = state
	s.mu.Unlock()
}

func (s *TLSStateStore) Get(host string) (TLSState, bool) {
	if s == nil {
		return TLSState{}, false
	}
	s.mu.RLock()
	state, ok := s.states[strings.ToLower(host)]
	s.mu.RUnlock()
	return state, ok
}
