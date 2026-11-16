package config

import (
	"strings"
	"sync"

	http "github.com/bogdanfinn/fhttp"
)

type CookieJar interface {
	http.CookieJar
	GetAllCookies() map[string][]*http.Cookie
}

type RetryBudget struct {
	max    int
	mu     sync.Mutex
	counts map[string]int
}

func NewRetryBudget(max int) *RetryBudget {
	return &RetryBudget{max: max, counts: make(map[string]int)}
}

func (b *RetryBudget) Take(host string) bool {
	if b == nil || b.max <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts[host] >= b.max {
		return false
	}
	b.counts[host]++
	return true
}

func (o *Options) NormalizeCookie() {
	if o == nil || o.Cookie == "" {
		return
	}
	if strings.Contains(o.Cookie, "=") {
		o.CookieHeader = o.Cookie
	} else {
		o.CookieFile = o.Cookie
	}
	o.Cookie = ""
}
