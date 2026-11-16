package httpx

import (
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	"golang.org/x/net/publicsuffix"
)

type trackedCookieJar struct {
	jar     *cookiejar.Jar
	mu      sync.RWMutex
	cookies map[string]trackedCookie
}

type trackedCookie struct {
	group  string
	cookie *http.Cookie
}

func newTrackedCookieJar() *trackedCookieJar {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &trackedCookieJar{jar: jar, cookies: make(map[string]trackedCookie)}
}

func (j *trackedCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.jar.SetCookies(u, cookies)
	now := time.Now()
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, cookie := range cookies {
		if !validTrackedCookie(u, cookie) {
			continue
		}
		group := cookieGroup(u, cookie)
		key := cookieKey(group, cookie)
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(now)) {
			delete(j.cookies, key)
			continue
		}
		copied := *cookie
		if copied.MaxAge > 0 && copied.Expires.IsZero() {
			copied.Expires = now.Add(time.Duration(copied.MaxAge) * time.Second)
		}
		j.cookies[key] = trackedCookie{group: group, cookie: &copied}
	}
}

func (j *trackedCookieJar) Cookies(u *url.URL) []*http.Cookie {
	return j.jar.Cookies(u)
}

func (j *trackedCookieJar) GetAllCookies() map[string][]*http.Cookie {
	now := time.Now()
	j.mu.Lock()
	defer j.mu.Unlock()
	all := make(map[string][]*http.Cookie)
	for key, tracked := range j.cookies {
		if !tracked.cookie.Expires.IsZero() && tracked.cookie.Expires.Before(now) {
			delete(j.cookies, key)
			continue
		}
		copied := *tracked.cookie
		all[tracked.group] = append(all[tracked.group], &copied)
	}
	return all
}

func cookieGroup(u *url.URL, cookie *http.Cookie) string {
	if cookie.Domain != "" {
		return normalizeCookieHost(cookie.Domain)
	}
	if u == nil {
		return ""
	}
	return normalizeCookieHost(u.Hostname())
}

func cookieKey(group string, cookie *http.Cookie) string {
	pathValue := cookie.Path
	if pathValue == "" {
		pathValue = "/"
	}
	return group + "\x00" + pathValue + "\x00" + cookie.Name
}

func validTrackedCookie(u *url.URL, cookie *http.Cookie) bool {
	if u == nil || cookie == nil || cookie.Name == "" {
		return false
	}
	if hasCookieControl(cookie.Name) || hasCookieControl(cookie.Value) || hasCookieControl(cookie.Path) {
		return false
	}
	host := normalizeCookieHost(u.Hostname())
	if host == "" {
		return false
	}
	if cookie.Domain == "" {
		return true
	}
	domain := normalizeCookieHost(cookie.Domain)
	if domain == "" || hasCookieControl(domain) || net.ParseIP(domain) != nil {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	if domain != host && !strings.HasSuffix(host, "."+domain) {
		return false
	}
	publicSuffix, _ := publicsuffix.PublicSuffix(domain)
	return publicSuffix != domain
}

func hasCookieControl(value string) bool {
	return strings.ContainsAny(value, "\x00\t\r\n")
}
