package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"gocurl/internal/config"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func HasCustomDNS(o *config.Options) bool {
	return o != nil && (len(o.DnsServers) > 0 || o.DohURL != "")
}

func dnsTimeout(o *config.Options) time.Duration {
	if o != nil && o.ConnectTimeout > 0 {
		return o.ConnectTimeout
	}
	return 10 * time.Second
}

func dnsQueryTypes(o *config.Options) []dnsmessage.Type {
	if o == nil {
		return []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}
	}
	if o.Ipv4 && o.Ipv6 {
		return []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}
	}
	var types []dnsmessage.Type
	if !o.Ipv6 {
		types = append(types, dnsmessage.TypeA)
	}
	if !o.Ipv4 {
		types = append(types, dnsmessage.TypeAAAA)
	}
	return types
}

func buildDNSQuery(host string, types []dnsmessage.Type) ([]byte, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(strings.ToLower(host), ".") + ".")
	if err != nil {
		return nil, err
	}
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               binary.BigEndian.Uint16(idBytes[:]),
		RecursionDesired: true,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	for _, typ := range types {
		if err := builder.Question(dnsmessage.Question{Name: name, Type: typ, Class: dnsmessage.ClassINET}); err != nil {
			return nil, err
		}
	}
	return builder.Finish()
}

func validateDNSResponse(query, response []byte) error {
	var qp dnsmessage.Parser
	qh, err := qp.Start(query)
	if err != nil {
		return err
	}
	var questions []dnsmessage.Question
	for {
		q, err := qp.Question()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return err
		}
		questions = append(questions, q)
	}
	var rp dnsmessage.Parser
	rh, err := rp.Start(response)
	if err != nil {
		return err
	}
	if qh.ID != rh.ID {
		return fmt.Errorf("DNS response ID mismatch")
	}
	for i := range questions {
		q, err := rp.Question()
		if err != nil {
			return fmt.Errorf("DNS response question mismatch")
		}
		if !strings.EqualFold(strings.TrimSuffix(q.Name.String(), "."), strings.TrimSuffix(questions[i].Name.String(), ".")) ||
			q.Type != questions[i].Type || q.Class != questions[i].Class {
			return fmt.Errorf("DNS response question mismatch")
		}
	}
	if _, err := rp.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return fmt.Errorf("DNS response question count mismatch")
	}
	return nil
}

func parseDNSResponse(msg []byte) ([]net.IP, bool, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(msg)
	if err != nil {
		return nil, false, err
	}
	if header.RCode != dnsmessage.RCodeSuccess {
		return nil, header.Truncated, fmt.Errorf("DNS response code %s", header.RCode)
	}
	_ = parser.SkipAllQuestions()
	var ips []net.IP
	for {
		resource, err := parser.Answer()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, header.Truncated, err
		}
		switch r := resource.Body.(type) {
		case *dnsmessage.AResource:
			ips = append(ips, net.IPv4(r.A[0], r.A[1], r.A[2], r.A[3]))
		case *dnsmessage.AAAAResource:
			ip := make(net.IP, net.IPv6len)
			copy(ip, r.AAAA[:])
			ips = append(ips, ip)
		}
	}
	return dedupeIPs(ips), header.Truncated, nil
}

func dedupeIPs(ips []net.IP) []net.IP {
	seen := make(map[string]bool, len(ips))
	out := ips[:0]
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		key := ip.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ip)
	}
	return out
}

func queryDNSServer(ctx context.Context, server string, msg []byte, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		response := append([]byte(nil), buf[:n]...)
		if err := validateDNSResponse(msg, response); err != nil {
			continue
		}
		_, truncated, parseErr := parseDNSResponse(response)
		if parseErr == nil && !truncated {
			return response, nil
		}
		if parseErr != nil && !truncated {
			return nil, parseErr
		}
		return queryDNSTCP(ctx, server, msg, timeout)
	}
}

func queryDNSTCP(ctx context.Context, server string, msg []byte, timeout time.Duration) ([]byte, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(msg)))
	if _, err := conn.Write(append(length[:], msg...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return nil, err
	}
	response := make([]byte, binary.BigEndian.Uint16(length[:]))
	if _, err := io.ReadFull(conn, response); err != nil {
		return nil, err
	}
	if err := validateDNSResponse(msg, response); err != nil {
		return nil, err
	}
	return response, nil
}

func queryDoH(ctx context.Context, o *config.Options, msg []byte) ([]byte, error) {
	u, err := url.Parse(o.DohURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("option --doh-url: HTTPS is required, got %q", u.Scheme)
	}
	if u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("option --doh-url: invalid URL")
	}
	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost, u.String(), bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	transport := &stdhttp.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: o.DohInsecure, MinVersion: tls.VersionTLS12},
	}
	proxyRaw, err := ProxyURLFor(o, u)
	if err != nil {
		return nil, err
	}
	control := denyPrivateControl(o)
	if proxyRaw != "" {
		proxyURL, err := url.Parse(proxyRaw)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(proxyURL.Scheme) {
		case "http", "https":
			transport.Proxy = stdhttp.ProxyURL(proxyURL)
		default:
			return nil, fmt.Errorf("DoH does not support %s proxies", proxyURL.Scheme)
		}
	}
	if control != nil {
		// Applies to the DoH server as well as to any proxy hop.
		transport.DialContext = (&net.Dialer{Control: control}).DialContext
	}
	defer transport.CloseIdleConnections()
	client := &stdhttp.Client{Transport: transport, Timeout: dnsTimeout(o)}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DoH server returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
	if err != nil {
		return nil, err
	}
	if err := validateDNSResponse(msg, body); err != nil {
		return nil, err
	}
	return body, nil
}

func ResolveHostIPs(ctx context.Context, o *config.Options, host string) ([]net.IP, error) {
	if o == nil {
		o = config.NewOptions()
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []net.IP{ip}, nil
	}
	types := dnsQueryTypes(o)
	if len(types) == 0 {
		return nil, fmt.Errorf("no DNS query type enabled")
	}
	msg, err := buildDNSQuery(host, types)
	if err != nil {
		return nil, err
	}
	timeout := dnsTimeout(o)
	if o.DohURL != "" {
		response, err := queryDoH(ctx, o, msg)
		if err != nil {
			return nil, err
		}
		ips, _, err := parseDNSResponse(response)
		if err != nil {
			return nil, err
		}
		ips = filterIPs(ips, o)
		if len(ips) == 0 {
			return nil, fmt.Errorf("DoH returned no usable addresses for %s", host)
		}
		return ips, nil
	}
	var lastErr error
	for _, server := range o.DnsServers {
		response, err := queryDNSServer(ctx, server, msg, timeout)
		if err != nil {
			lastErr = err
			continue
		}
		ips, _, err := parseDNSResponse(response)
		if err != nil {
			lastErr = err
			continue
		}
		if ips = filterIPs(ips, o); len(ips) > 0 {
			return ips, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses found for %s", host)
	}
	return nil, lastErr
}

func filterIPs(ips []net.IP, o *config.Options) []net.IP {
	if o == nil {
		o = config.NewOptions()
	}
	var out []net.IP
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if o.DenyPrivateAddresses && privateAddressDenied(ip) {
			continue
		}
		if o.Ipv4 && o.Ipv6 {
			out = append(out, ip)
			continue
		}
		if ip.To4() != nil {
			if o.Ipv6 {
				continue
			}
		} else if o.Ipv4 {
			continue
		}
		out = append(out, ip)
	}
	return dedupeIPs(out)
}
