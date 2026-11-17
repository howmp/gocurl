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
	Port             string
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

// SetOrigin stores state keyed by host and port, so two services on the same
// host but different ports never overwrite each other's TLS metadata. A
// host-level entry is recorded as well, for callers that only know the host.
func (s *TLSStateStore) SetOrigin(host, port string, state TLSState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.states == nil {
		s.states = make(map[string]TLSState)
	}
	s.states[originKey(host, port)] = state
	if _, exists := s.states[strings.ToLower(host)]; !exists {
		s.states[strings.ToLower(host)] = state
	}
	s.mu.Unlock()
}

func (s *TLSStateStore) GetOrigin(host, port string) (TLSState, bool) {
	state, _, ok := s.LookupOrigin(host, port)
	return state, ok
}

// LookupOrigin reports whether the state came from an exact host:port entry
// rather than the host-level fallback.
func (s *TLSStateStore) LookupOrigin(host, port string) (TLSState, bool, bool) {
	if s == nil {
		return TLSState{}, false, false
	}
	key := originKey(host, port)
	s.mu.RLock()
	state, ok := s.states[key]
	s.mu.RUnlock()
	if ok {
		return state, true, true
	}
	fallback, ok := s.Get(host)
	return fallback, false, ok
}

func originKey(host, port string) string {
	host = strings.ToLower(host)
	if port == "" {
		return host
	}
	return host + ":" + port
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
