// Package mdns answers multicast DNS queries for the mirror's hostname, so
// magicmirror.local resolves on any network it joins.
//
// Implemented here rather than by shipping avahi. Avahi wants D-Bus, expat
// and libdaemon — a couple of megabytes and three moving parts to answer one
// question about one name. The app is already running, already knows its own
// address, and this is a few hundred lines with no image cost.
//
// The tradeoff worth stating: the name only resolves while the app is
// running. mm-supervise respawns a crashed app in about a second, so the gap
// is small — but a device stuck in a crash loop will not answer to its name,
// and you would need its IP address. That is an acceptable trade for a
// convenience feature; it would not be for the only way in.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Multicast DNS group and port, from RFC 6762.
var (
	groupIPv4 = net.IPv4(224, 0, 0, 251)
	addrIPv4  = &net.UDPAddr{IP: groupIPv4, Port: 5353}
)

const (
	// defaultTTL for our address records, in seconds. RFC 6762 recommends
	// 120s for records that can change, which an address on a device people
	// move between networks certainly can.
	defaultTTL = 120

	// classFlushCache is the top bit of the class field in a response,
	// telling resolvers to replace rather than append to what they hold.
	// Without it, a mirror that changes address leaves the old one cached
	// and the name resolves to somewhere it no longer is.
	classFlushCache = 1 << 15

	// qClassUnicast is the top bit of the class field in a *question*,
	// asking for a unicast reply. Some resolvers set it for their first
	// query, and ignoring it makes the first lookup fail.
	qClassUnicast = 1 << 15
)

// Responder answers queries for a single hostname.
type Responder struct {
	// Host is the name to answer for, without the .local suffix.
	Host string

	// TTL for published records.
	TTL uint32

	Log *slog.Logger

	// Addrs reports the addresses to advertise. Injected so tests do not
	// depend on the machine's real interfaces.
	Addrs func() []net.IP

	fqdn string
}

// New returns a Responder for host, e.g. "magicmirror".
func New(host string, log *slog.Logger) *Responder {
	return &Responder{
		Host:  host,
		TTL:   defaultTTL,
		Log:   log,
		Addrs: localAddrs,
	}
}

// Run answers queries until ctx is cancelled.
//
// Never fatal to the caller: mDNS is a convenience, and a mirror that cannot
// join a multicast group should still show the time.
func (r *Responder) Run(ctx context.Context) {
	if r.Host == "" {
		r.Host = "magicmirror"
	}
	if r.TTL == 0 {
		r.TTL = defaultTTL
	}
	r.fqdn = strings.ToLower(r.Host) + ".local."

	// Keep trying to join the multicast group.
	//
	// The app starts within seconds of boot, well before wpa_supplicant has
	// associated and udhcpc has assigned an address, so the first attempt
	// essentially always fails. An earlier version logged a warning and
	// returned — which meant mDNS was dead for the entire session, every
	// session, and magicmirror.local never resolved once.
	conn := r.listen(ctx)
	if conn == nil {
		return
	}
	defer conn.Close()

	// Read buffer sized for a standard mDNS packet; anything larger is not
	// something we need to answer.
	_ = conn.SetReadBuffer(65536)

	r.Log.Info("mDNS responder started", "name", r.fqdn)

	// Announce unsolicited on startup so resolvers learn the name without
	// having to ask, and again shortly after — RFC 6762 suggests repeating,
	// since the first announcement can be lost while the link settles.
	go r.announceRepeatedly(ctx, conn)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			r.Log.Debug("mDNS read failed", "err", err)
			continue
		}
		r.handle(conn, src, buf[:n])
	}
}

// listen joins the mDNS multicast group, retrying until it succeeds or ctx
// is cancelled. Returns nil only on cancellation.
func (r *Responder) listen(ctx context.Context) *net.UDPConn {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for attempt := 1; ; attempt++ {
		conn, err := net.ListenMulticastUDP("udp4", nil, addrIPv4)
		if err == nil {
			if attempt > 1 {
				r.Log.Info("mDNS group joined", "attempts", attempt)
			}
			return conn
		}

		// Only worth a warning once; after that it is just noise in a log
		// that matters.
		if attempt == 1 {
			r.Log.Debug("mDNS not ready yet; will keep trying", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// announceRepeatedly sends gratuitous responses on a schedule.
//
// Also re-announces when the address changes, which is the case that matters
// for a device carried between networks: without it the name keeps resolving
// to the previous network's address until the old record ages out.
func (r *Responder) announceRepeatedly(ctx context.Context, conn *net.UDPConn) {
	var last string

	send := func() {
		ips := r.Addrs()
		if len(ips) == 0 {
			return
		}
		key := fmt.Sprint(ips)
		if key != last {
			r.Log.Info("announcing mDNS name", "name", r.fqdn, "addrs", key)
			last = key
		}
		if err := r.respond(conn, addrIPv4, ips, 0); err != nil {
			r.Log.Debug("mDNS announce failed", "err", err)
		}
	}

	// Startup burst, then a slow heartbeat.
	for _, d := range []time.Duration{0, time.Second, 2 * time.Second} {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
			send()
		}
	}

	ticker := time.NewTicker(time.Duration(r.TTL/2) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// handle parses a query and replies if it asks about our name.
func (r *Responder) handle(conn *net.UDPConn, src *net.UDPAddr, packet []byte) {
	var p dnsmessage.Parser
	header, err := p.Start(packet)
	if err != nil || header.Response {
		// Malformed, or someone else's answer. Either way not ours.
		return
	}

	questions, err := p.AllQuestions()
	if err != nil {
		return
	}

	wantUnicast := false
	matched := false

	for _, q := range questions {
		if !strings.EqualFold(q.Name.String(), r.fqdn) {
			continue
		}
		switch q.Type {
		case dnsmessage.TypeA, dnsmessage.TypeALL:
			matched = true
			// The top class bit asks for a unicast reply.
			if uint16(q.Class)&qClassUnicast != 0 {
				wantUnicast = true
			}
		}
	}
	if !matched {
		return
	}

	ips := r.Addrs()
	if len(ips) == 0 {
		return
	}

	dst := addrIPv4
	if wantUnicast {
		dst = src
	}
	if err := r.respond(conn, dst, ips, header.ID); err != nil {
		r.Log.Debug("mDNS respond failed", "err", err)
	}
}

// respond writes an mDNS answer containing our A records.
func (r *Responder) respond(conn *net.UDPConn, dst *net.UDPAddr, ips []net.IP, id uint16) error {
	name, err := dnsmessage.NewName(r.fqdn)
	if err != nil {
		return err
	}

	msg := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:            id,
			Response:      true,
			Authoritative: true,
		},
	}

	for _, ip := range ips {
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		var a [4]byte
		copy(a[:], v4)

		msg.Answers = append(msg.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{
				Name: name,
				Type: dnsmessage.TypeA,
				// Cache-flush so a resolver replaces any address it already
				// holds for us rather than keeping both.
				Class: dnsmessage.Class(uint16(dnsmessage.ClassINET) | classFlushCache),
				TTL:   r.TTL,
			},
			Body: &dnsmessage.AResource{A: a},
		})
	}
	if len(msg.Answers) == 0 {
		return nil
	}

	out, err := msg.Pack()
	if err != nil {
		return err
	}
	_, err = conn.WriteToUDP(out, dst)
	return err
}

// localAddrs returns the routable IPv4 addresses to advertise.
func localAddrs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []net.IP
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
				continue
			}
			// 192.168.4.1 is the setup portal's own address. Advertising it
			// would point the name at an interface that disappears as soon
			// as provisioning finishes.
			if v4.String() == "192.168.4.1" {
				continue
			}
			out = append(out, v4)
		}
	}
	return out
}
