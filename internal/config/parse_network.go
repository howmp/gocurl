package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

func parseConnectToOption(o *Options, raw string) error {
	parts, err := splitConnectTo(raw)
	if err != nil {
		return err
	}
	host := NormalizeResolveHost(parts[0])
	targetHost := NormalizeResolveHost(parts[2])
	if host == "" || parts[1] == "" || targetHost == "" || parts[3] == "" {
		return fmt.Errorf("option --connect-to: invalid value %q", raw)
	}
	if !validConfigHost(host) || !validConfigHost(targetHost) {
		return fmt.Errorf("option --connect-to: invalid host in %q", raw)
	}
	if err := validateConfigPort(parts[1]); err != nil {
		return fmt.Errorf("option --connect-to: invalid source port in %q", raw)
	}
	if err := validateConfigPort(parts[3]); err != nil {
		return fmt.Errorf("option --connect-to: invalid target port in %q", raw)
	}
	o.ConnectTo[host+":"+parts[1]] = net.JoinHostPort(targetHost, parts[3])
	return nil
}

func validConfigHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " \t\r\n") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return httpguts.ValidHostHeader(host)
}

func validateConfigPort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port")
	}
	return nil
}

func ValidateConnectTarget(target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || !validConfigHost(host) {
		return fmt.Errorf("invalid CONNECT target")
	}
	if err := validateConfigPort(port); err != nil {
		return fmt.Errorf("invalid CONNECT target port")
	}
	return nil
}

func ValidateResolveMaps(resolves map[string][]string, connectTo map[string]string) error {
	for key, addrs := range resolves {
		host, port, ok := splitConfigHostPort(key)
		if !ok || !validConfigHost(host) || validateConfigPort(port) != nil {
			return fmt.Errorf("invalid resolve entry")
		}
		if len(addrs) == 0 {
			return fmt.Errorf("resolve entry has no addresses")
		}
		for _, addr := range addrs {
			if net.ParseIP(addr) == nil {
				return fmt.Errorf("invalid resolve address")
			}
		}
	}
	for key, target := range connectTo {
		host, port, ok := splitConfigHostPort(key)
		if !ok || !validConfigHost(host) || validateConfigPort(port) != nil {
			return fmt.Errorf("invalid connect-to entry")
		}
		if err := ValidateConnectTarget(target); err != nil {
			return err
		}
	}
	return nil
}

func splitConfigHostPort(value string) (host, port string, ok bool) {
	idx := strings.LastIndexByte(value, ':')
	if idx <= 0 || idx == len(value)-1 {
		return "", "", false
	}
	return value[:idx], value[idx+1:], true
}

func splitConnectTo(v string) ([]string, error) {
	var parts []string
	start := 0
	depth := 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				parts = append(parts, v[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, v[start:])
	if len(parts) != 4 {
		return nil, fmt.Errorf("option --connect-to: invalid value %q", v)
	}
	return parts, nil
}

func parseResolveOption(o *Options, raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fmt.Errorf("option --resolve: empty value")
	}
	remove := false
	switch value[0] {
	case '+':
		value = value[1:]
	case '-':
		remove = true
		value = value[1:]
	}
	parts := strings.SplitN(value, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("option --resolve: invalid value %q", raw)
	}
	host := NormalizeResolveHost(parts[0])
	if !validConfigHost(host) {
		return fmt.Errorf("option --resolve: invalid host in %q", raw)
	}
	if err := validateConfigPort(parts[1]); err != nil {
		return fmt.Errorf("option --resolve: invalid port in %q", raw)
	}
	key := host + ":" + parts[1]
	if remove {
		delete(o.Resolves, key)
		return nil
	}
	if len(parts) < 3 || parts[2] == "" {
		return fmt.Errorf("option --resolve: invalid value %q", raw)
	}
	var addrs []string
	for _, addr := range strings.Split(parts[2], ",") {
		addr = strings.TrimSpace(addr)
		addr = strings.TrimPrefix(addr, "[")
		addr = strings.TrimSuffix(addr, "]")
		if net.ParseIP(addr) == nil {
			return fmt.Errorf("option --resolve: invalid address in %q", raw)
		}
		addrs = append(addrs, addr)
	}
	o.Resolves[key] = addrs
	return nil
}

func NormalizeResolveHost(host string) string {
	host = strings.Trim(host, "[]")
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host
}

func validateRange(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("option --range: empty range")
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		start, end, ok := strings.Cut(part, "-")
		if !ok || (start == "" && end == "") {
			return fmt.Errorf("option --range: invalid range %q", value)
		}
		if start != "" && !allDigits(start) {
			return fmt.Errorf("option --range: invalid range %q", value)
		}
		if end != "" && !allDigits(end) {
			return fmt.Errorf("option --range: invalid range %q", value)
		}
		if start != "" && end != "" {
			startNum, _ := strconv.ParseInt(start, 10, 64)
			endNum, _ := strconv.ParseInt(end, 10, 64)
			if startNum > endNum {
				return fmt.Errorf("option --range: invalid range %q", value)
			}
		}
	}
	return nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseSize(v string) (int64, error) {
	value := strings.TrimSpace(v)
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	multiplier := int64(1)
	switch value[len(value)-1] {
	case 'k', 'K':
		multiplier = 1024
		value = value[:len(value)-1]
	case 'm', 'M':
		multiplier = 1024 * 1024
		value = value[:len(value)-1]
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
		value = value[:len(value)-1]
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size")
	}
	if n > (1<<63-1)/multiplier {
		return 0, fmt.Errorf("size overflow")
	}
	return n * multiplier, nil
}

func parseDNSServers(value string) ([]string, error) {
	var servers []string
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("option --dns-servers: empty server")
		}
		raw = strings.TrimPrefix(raw, "udp://")
		raw = strings.TrimPrefix(raw, "tcp://")
		server, err := normalizeDNSServer(raw)
		if err != nil {
			return nil, fmt.Errorf("option --dns-servers: %w", err)
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func normalizeDNSServer(raw string) (string, error) {
	host := raw
	port := "53"
	if h, p, err := net.SplitHostPort(raw); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = strings.Trim(raw, "[]")
	} else if strings.Count(raw, ":") == 1 {
		if idx := strings.LastIndexByte(raw, ':'); idx > 0 && net.ParseIP(raw[:idx]) != nil {
			host, port = raw[:idx], raw[idx+1:]
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", fmt.Errorf("invalid server %q", raw)
	}
	if net.ParseIP(host) == nil && strings.ContainsAny(host, "/ ") {
		return "", fmt.Errorf("invalid server %q", raw)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("invalid port in %q", raw)
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}
