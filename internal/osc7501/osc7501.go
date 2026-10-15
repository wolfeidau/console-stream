// Package osc7501 parses the OSC 7501 "program status" terminal protocol
// (rev 0.4, https://www.superlogical.com/rex/docs/build/program-status).
//
// A report has the form ESC ] 7501 ; key=value:key=value ST where ST is
// ESC \ or BEL. Parse decodes a report body, Report.Encode produces a
// canonical sequence, and Scanner is an io.Writer filter that recognises
// reports in a byte stream while forwarding every byte unchanged.
//
// Known limitation: Scanner tracks only OSC framing, so an ESC ] that
// appears inside a DCS or APC string can be mis-framed.
package osc7501

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Protocol limits.
const (
	maxSequence   = 4096
	maxKeyLen     = 16
	maxIDLen      = 128
	maxIDLevels   = 8
	maxSegmentLen = 32
	maxTitleEnc   = 256
	maxTitleDec   = 192
	maxMsgEnc     = 2732
	maxMsgDec     = 2048
)

// QueryReply is the feature-detection reply. It lists the rev 0.4 default
// states and kinds, so its body differs from the query: if the PTY echoes it
// back, Scanner does not mistake it for a new query (no reply loop).
const QueryReply = "\x1b]7501;?:states=idle,working,done,blocked,error:kinds=permission,question,auth\x1b\\"

// Report is a decoded program status report.
type Report struct {
	State, ID, Kind, App, Title, Msg string // Title and Msg are decoded
	Progress                         int    // 0-100, or -1 if indeterminate
}

// Parse decodes the body of a report (the bytes between "7501;" and ST).
// It returns false if the whole report must be ignored.
func Parse(body []byte) (Report, bool) {
	vals := map[string]string{}
	for _, pair := range bytes.Split(body, []byte{':'}) {
		k, v, ok := bytes.Cut(pair, []byte{'='})
		if !ok {
			continue
		}
		key := string(bytes.TrimSpace(k))
		val := string(bytes.TrimSpace(v))
		if !validKey(key) || !validValue(val) {
			continue
		}
		vals[key] = val
	}

	r := Report{Progress: -1, State: vals["state"]}
	switch r.State {
	case "idle", "working", "done", "blocked", "error", "clear":
	default:
		return Report{}, false
	}

	if id, ok := vals["id"]; ok {
		if !ValidID(id) {
			return Report{}, false
		}
		r.ID = id
	}

	if r.State == "blocked" {
		switch k := vals["kind"]; k {
		case "permission", "question", "auth":
			r.Kind = k
		}
	}

	if r.State == "working" || r.State == "blocked" {
		if p, ok := vals["progress"]; ok {
			r.Progress = parseProgress(p)
		}
	}

	if a, ok := vals["app"]; ok && ValidSegment(a) {
		r.App = a
	}

	var ok bool
	if s, present := vals["title"]; present {
		if r.Title, ok = decodeText(s, maxTitleEnc, maxTitleDec); !ok {
			return Report{}, false
		}
	}
	if s, present := vals["msg"]; present {
		if r.Msg, ok = decodeText(s, maxMsgEnc, maxMsgDec); !ok {
			return Report{}, false
		}
	}
	return r, true
}

// Encode returns the full report sequence terminated by ESC \.
// Keys are emitted in canonical order; empty values and a progress of -1
// are omitted.
func (r Report) Encode() []byte {
	var b strings.Builder
	b.WriteString("\x1b]7501;state=" + r.State)
	add := func(k, v string) {
		if v != "" {
			b.WriteString(":" + k + "=" + v)
		}
	}
	add("id", r.ID)
	add("kind", r.Kind)
	if r.Progress >= 0 {
		add("progress", strconv.Itoa(r.Progress))
	}
	add("app", r.App)
	if r.Title != "" {
		add("title", base64.StdEncoding.EncodeToString([]byte(r.Title)))
	}
	if r.Msg != "" {
		add("msg", base64.StdEncoding.EncodeToString([]byte(r.Msg)))
	}
	b.WriteString("\x1b\\")
	return []byte(b.String())
}

func validKey(k string) bool {
	if k == "" || len(k) > maxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 'a' || k[i] > 'z' {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func validValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if !isAlnum(v[i]) && strings.IndexByte("_.,+/=-", v[i]) < 0 {
			return false
		}
	}
	return true
}

// ValidSegment reports whether s matches [A-Za-z0-9_.+-]{1,32}.
func ValidSegment(s string) bool {
	if s == "" || len(s) > maxSegmentLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isAlnum(s[i]) && strings.IndexByte("_.+-", s[i]) < 0 {
			return false
		}
	}
	return true
}

func ValidID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	segs := strings.Split(id, "/")
	if len(segs) > maxIDLevels {
		return false
	}
	for _, s := range segs {
		if !ValidSegment(s) {
			return false
		}
	}
	return true
}

func parseProgress(s string) int {
	if s == "" || len(s) > 3 {
		return -1
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > 100 {
		return -1
	}
	return n
}

// decodeText decodes base64 (padding optional) UTF-8 text enforcing limits.
func decodeText(s string, maxEnc, maxDec int) (string, bool) {
	if len(s) > maxEnc {
		return "", false
	}
	enc := base64.RawStdEncoding
	if strings.HasSuffix(s, "=") {
		enc = base64.StdEncoding
	}
	raw, err := enc.DecodeString(s)
	if err != nil || len(raw) > maxDec || !utf8.Valid(raw) {
		return "", false
	}
	for _, c := range string(raw) {
		if c < 0x20 || c == 0x7f || (c >= 0x80 && c <= 0x9f) {
			return "", false
		}
	}
	return string(raw), true
}
