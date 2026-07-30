package ics

import "strings"

// NormalizeURL turns a subscription link into something an HTTP client can
// fetch, without changing links that are already fine.
//
// Calendar apps hand out webcal:// links — it is what Apple's share sheet
// produces, what Google's "public address in iCal format" copies as on some
// platforms, and what Outlook offers as "subscribe". The scheme is not a
// protocol: it means "this is HTTP, and it is a calendar, so hand it to the
// calendar app rather than the browser". Go's http client rejects it outright
// with "unsupported protocol scheme", and the user has been given no reason
// to suspect the link they pasted needs editing.
//
// webcals:// is the explicitly-secure spelling. Plain webcal:// is treated as
// https too: every provider that emits these serves TLS, and quietly
// downgrading a calendar credential to cleartext is not a favour.
//
// A pasted string with no scheme at all gets https, since a share sheet's
// text can lose the prefix in transit. Anything else — including schemes we
// have no business rewriting, like file:// — is returned untouched, to be
// rejected downstream where the error can name the problem.
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	if i := strings.Index(s, "://"); i >= 0 {
		switch strings.ToLower(s[:i]) {
		case "webcal", "webcals":
			return "https" + s[i:]
		}
		return s
	}

	// No scheme. A bare "mailto:x" or "file:/tmp" keeps its colon-form and is
	// left alone; a bare host is assumed to be a web address.
	if strings.Contains(s, ":") && !strings.Contains(s[:strings.IndexByte(s, ':')], "/") {
		return s
	}
	return "https://" + s
}
