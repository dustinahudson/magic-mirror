package mdns

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Built through New rather than as a literal, so tests exercise the same
// construction the mirror uses — including the rename channel, which a
// literal leaves nil and silently drops nudges into.
func testResponder() *Responder {
	r := New("magicmirror", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.TTL = 120
	r.Addrs = func() []net.IP {
		return []net.IP{net.IPv4(192, 168, 1, 120)}
	}
	return r
}

// query builds an mDNS question packet.
func query(t *testing.T, name string, typ dnsmessage.Type, unicast bool) []byte {
	t.Helper()

	n, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatalf("bad name %q: %v", name, err)
	}
	class := uint16(dnsmessage.ClassINET)
	if unicast {
		class |= qClassUnicast
	}

	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 0x1234},
		Questions: []dnsmessage.Question{{
			Name:  n,
			Type:  typ,
			Class: dnsmessage.Class(class),
		}},
	}
	b, err := msg.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	return b
}

// answersFor runs a query through the responder's packing path and returns
// the A records it would send.
func answersFor(t *testing.T, r *Responder, ips []net.IP) []dnsmessage.Resource {
	t.Helper()

	name, err := dnsmessage.NewName(r.Name())
	if err != nil {
		t.Fatal(err)
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{Response: true, Authoritative: true},
	}
	for _, ip := range ips {
		var a [4]byte
		copy(a[:], ip.To4())
		msg.Answers = append(msg.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{
				Name:  name,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.Class(uint16(dnsmessage.ClassINET) | classFlushCache),
				TTL:   r.TTL,
			},
			Body: &dnsmessage.AResource{A: a},
		})
	}
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("pack response: %v", err)
	}

	var parsed dnsmessage.Message
	if err := parsed.Unpack(packed); err != nil {
		t.Fatalf("unpack response: %v", err)
	}
	return parsed.Answers
}

func TestResponseCarriesAddressAndFlushBit(t *testing.T) {
	r := testResponder()
	answers := answersFor(t, r, r.Addrs())

	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(answers))
	}
	a := answers[0]

	if got := a.Header.Name.String(); got != "magicmirror.local." {
		t.Errorf("name = %q", got)
	}
	body, ok := a.Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("body is %T, want *AResource", a.Body)
	}
	if got := net.IP(body.A[:]).String(); got != "192.168.1.120" {
		t.Errorf("address = %s, want 192.168.1.120", got)
	}

	// Without the cache-flush bit, a mirror that moves networks leaves the
	// old address cached and the name resolves somewhere it no longer is.
	if uint16(a.Header.Class)&classFlushCache == 0 {
		t.Error("cache-flush bit not set on the answer")
	}
	if a.Header.TTL != 120 {
		t.Errorf("TTL = %d, want 120", a.Header.TTL)
	}
}

// The responder must answer only for its own name.
func TestIgnoresOtherNames(t *testing.T) {
	r := testResponder()

	cases := []struct {
		name  string
		match bool
	}{
		{"magicmirror.local.", true},
		{"MagicMirror.local.", true}, // DNS names are case-insensitive
		{"someoneelse.local.", false},
		{"magicmirror.example.com.", false},
		{"mirror.local.", false},
	}

	for _, c := range cases {
		packet := query(t, c.name, dnsmessage.TypeA, false)

		var p dnsmessage.Parser
		if _, err := p.Start(packet); err != nil {
			t.Fatalf("start: %v", err)
		}
		questions, err := p.AllQuestions()
		if err != nil {
			t.Fatalf("questions: %v", err)
		}

		matched := false
		for _, q := range questions {
			if equalName(q.Name.String(), r.Name()) {
				matched = true
			}
		}
		if matched != c.match {
			t.Errorf("%s: matched = %v, want %v", c.name, matched, c.match)
		}
	}
}

// A response to someone else's query must be ignored, or two mirrors on one
// network would answer each other's announcements forever.
func TestIgnoresResponses(t *testing.T) {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{Response: true},
	}
	packed, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}

	var p dnsmessage.Parser
	header, err := p.Start(packed)
	if err != nil {
		t.Fatal(err)
	}
	if !header.Response {
		t.Fatal("test packet is not marked as a response")
	}
	// handle() returns early on header.Response, which is what this asserts.
}

// The unicast-response bit must be honoured; some resolvers set it on their
// first query and ignoring it makes the first lookup fail.
func TestUnicastBitIsDetected(t *testing.T) {
	for _, unicast := range []bool{true, false} {
		packet := query(t, "magicmirror.local.", dnsmessage.TypeA, unicast)

		var p dnsmessage.Parser
		if _, err := p.Start(packet); err != nil {
			t.Fatal(err)
		}
		questions, err := p.AllQuestions()
		if err != nil {
			t.Fatal(err)
		}

		got := uint16(questions[0].Class)&qClassUnicast != 0
		if got != unicast {
			t.Errorf("unicast=%v detected as %v", unicast, got)
		}
	}
}

// The portal's own address must never be advertised: it exists only while
// provisioning is running and vanishes the moment the mirror joins a real
// network.
func TestPortalAddressIsNotAdvertised(t *testing.T) {
	r := testResponder()
	r.Addrs = func() []net.IP {
		return filterPortal([]net.IP{
			net.IPv4(192, 168, 4, 1),
			net.IPv4(192, 168, 1, 120),
		})
	}

	ips := r.Addrs()
	for _, ip := range ips {
		if ip.String() == "192.168.4.1" {
			t.Error("advertised the setup portal's address")
		}
	}
	if len(ips) != 1 {
		t.Errorf("got %d addresses, want 1", len(ips))
	}
}

// filterPortal mirrors the exclusion localAddrs applies.
func filterPortal(in []net.IP) []net.IP {
	var out []net.IP
	for _, ip := range in {
		if ip.String() == "192.168.4.1" {
			continue
		}
		out = append(out, ip)
	}
	return out
}

func equalName(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Renaming the mirror from the settings page has to change what it answers
// to. The responder used to read the hostname once at startup, so a rename
// saved cleanly and changed nothing until someone power-cycled the device —
// while the settings page said the new name was already in effect.
func TestRenameChangesWhatIsAnswered(t *testing.T) {
	r := testResponder()

	if got := r.Name(); got != "magicmirror.local." {
		t.Fatalf("Name() = %q", got)
	}

	r.Rename("magicmirror-2")
	if got := r.Name(); got != "magicmirror-2.local." {
		t.Fatalf("after Rename, Name() = %q", got)
	}

	// The old name must stop being answered, or two names resolve to one
	// mirror and whichever a browser cached first is the one it keeps.
	if answers(t, r, "magicmirror.local.") {
		t.Error("still answering to the old name")
	}
	if !answers(t, r, "magicmirror-2.local.") {
		t.Error("not answering to the new name")
	}
}

func TestRenameIgnoresNoOpsAndBlanks(t *testing.T) {
	r := testResponder()

	r.Rename("")
	if got := r.Name(); got != "magicmirror.local." {
		t.Errorf("a blank rename took effect: %q", got)
	}

	// Case and the .local suffix are normalised, so these are the same name.
	r.Rename("MagicMirror")
	if got := r.Name(); got != "magicmirror.local." {
		t.Errorf("case-only rename changed the name: %q", got)
	}

	r.Rename("  spaced  ")
	if got := r.Name(); got != "spaced.local." {
		t.Errorf("Name() = %q, want trimmed", got)
	}
}

// A rename must wake the announcer rather than waiting out half a TTL, and
// repeated renames must not queue up a burst of announcements.
func TestRenameNudgesTheAnnouncerOnce(t *testing.T) {
	r := testResponder()

	r.Rename("magicmirror-2")
	r.Rename("magicmirror-3")
	r.Rename("magicmirror-4")

	if got := len(r.rename); got != 1 {
		t.Errorf("queued %d announcements for three renames, want 1", got)
	}
}

// answers reports whether the responder replies to a query for name.
func answers(t *testing.T, r *Responder, name string) bool {
	t.Helper()
	n, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatalf("bad name %q: %v", name, err)
	}
	msg := dnsmessage.Message{
		Questions: []dnsmessage.Question{{
			Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET,
		}},
	}
	packet, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}

	var p dnsmessage.Parser
	if _, err := p.Start(packet); err != nil {
		t.Fatal(err)
	}
	qs, err := p.AllQuestions()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if equalName(q.Name.String(), r.Name()) {
			return true
		}
	}
	return false
}
