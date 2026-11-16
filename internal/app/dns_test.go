package app

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func dnsTestReply(query []byte) []byte {
	var parser dnsmessage.Parser
	header, _ := parser.Start(query)
	var questions []dnsmessage.Question
	for {
		question, err := parser.Question()
		if err == dnsmessage.ErrSectionDone {
			break
		}
		if err != nil {
			break
		}
		questions = append(questions, question)
	}
	var builder dnsmessage.Builder
	builder = dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		RecursionDesired:   true,
		RecursionAvailable: true,
	})
	_ = builder.StartQuestions()
	for _, question := range questions {
		_ = builder.Question(question)
	}
	_ = builder.StartAnswers()
	answerName := dnsmessage.MustNewName("dns.test.")
	if len(questions) > 0 {
		answerName = questions[0].Name
	}
	_ = builder.AResource(dnsmessage.ResourceHeader{
		Name:  answerName,
		Class: dnsmessage.ClassINET,
		TTL:   60,
	}, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
	msg, _ := builder.Finish()
	return msg
}

func TestCustomDNSServers(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			reply := dnsTestReply(append([]byte(nil), buf[:n]...))
			_, _ = pc.WriteTo(reply, addr)
		}
	}()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "dns-ok")
	}))
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	code, out, stderr := runGocurl(t, "-sS",
		"--dns-servers", pc.LocalAddr().String(),
		"http://dns.test:"+port+"/")
	if code != 0 || out != "dns-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestDoHResolver(t *testing.T) {
	doh := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/dns-message" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		query, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(dnsTestReply(query))
	}))
	defer doh.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "doh-ok")
	}))
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	code, out, stderr := runGocurl(t, "-sS",
		"--doh-url", doh.URL+"/dns-query", "--doh-insecure",
		"http://doh.test:"+port+"/")
	if code != 0 || out != "doh-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestCustomDNSServersDaemon(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(dnsTestReply(append([]byte(nil), buf[:n]...)), addr)
		}
	}()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "daemon-dns-ok")
	}))
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	code, out, stderr := runGocurlMode(t, true, "-sS",
		"--dns-servers", pc.LocalAddr().String(),
		"http://daemon-dns.test:"+port+"/")
	if code != 0 || out != "daemon-dns-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}
