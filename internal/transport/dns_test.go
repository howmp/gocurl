package transport

import (
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestValidateDNSResponseRejectsMismatch(t *testing.T) {
	query, err := buildDNSQuery("example.test", []dnsmessage.Type{dnsmessage.TypeA})
	if err != nil {
		t.Fatal(err)
	}
	var qp dnsmessage.Parser
	h, _ := qp.Start(query)
	q, _ := qp.Question()
	build := func(id uint16, name dnsmessage.Name) []byte {
		var b dnsmessage.Builder
		b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true})
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
		_ = b.StartAnswers()
		_ = b.AResource(dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET}, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		msg, _ := b.Finish()
		return msg
	}
	if err := validateDNSResponse(query, build(h.ID+1, q.Name)); err == nil {
		t.Fatal("expected ID mismatch")
	}
	other, _ := dnsmessage.NewName("other.test.")
	if err := validateDNSResponse(query, build(h.ID, other)); err == nil {
		t.Fatal("expected question mismatch")
	}
	if err := validateDNSResponse(query, build(h.ID, q.Name)); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
}
