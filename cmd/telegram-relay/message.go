package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// Payload is the union of the documents GoAlert's webhook contact method
// sends (notification/webhook/sender.go upstream). Type says which fields
// are set.
type Payload struct {
	AppName     string
	Type        string
	AlertID     int
	Summary     string
	Details     string
	ServiceID   string
	ServiceName string
	Meta        map[string]string
	LogEntry    string
	Count       int
	Code        string
	GoAlertURL  string

	Users        []OnCallUser
	ScheduleID   string
	ScheduleName string
	ScheduleURL  string
}

// OnCallUser is one person in a ScheduleOnCallUsers notification.
type OnCallUser struct {
	ID   string
	Name string
	URL  string
}

// The payload types GoAlert sends. Anything else is refused, so a new type
// upstream shows up as a counted refusal rather than an empty message.
const (
	typeAlert             = "Alert"
	typeAlertBundle       = "AlertBundle"
	typeAlertStatus       = "AlertStatus"
	typeAlertStatusBundle = "AlertStatusBundle"
	typeVerification      = "Verification"
	typeTest              = "Test"
	typeScheduleOnCall    = "ScheduleOnCallUsers"
)

var knownTypes = []string{typeAlert, typeAlertBundle, typeAlertStatus, typeAlertStatusBundle, typeVerification, typeTest, typeScheduleOnCall}

// typeLabel is the metric label for a payload type: the type itself when
// known, "unknown" otherwise, so a sender cannot mint label values.
func typeLabel(t string) string {
	for _, k := range knownTypes {
		if t == k {
			return t
		}
	}
	return "unknown"
}

// maxDetails keeps a message under Telegram's 4096-character limit with
// room for everything around the details.
const maxDetails = 2500

// DecodePayload parses one webhook body.
func DecodePayload(body []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("not a GoAlert webhook document: %w", err)
	}
	if typeLabel(p.Type) == "unknown" {
		return p, fmt.Errorf("unsupported webhook type %q", p.Type)
	}
	return p, nil
}

// Format renders p as Telegram HTML. Everything that came from GoAlert is
// escaped: an alert summary is written by whoever raised the alert, and
// Telegram refuses the whole message on one stray `<`.
func Format(p Payload) string {
	app := p.AppName
	if app == "" {
		app = "GoAlert"
	}
	var b strings.Builder
	switch p.Type {
	case typeAlert:
		fmt.Fprintf(&b, "<b>%s alert #%d</b>", esc(app), p.AlertID)
		writeService(&b, p.ServiceName)
		fmt.Fprintf(&b, "\n\n<b>%s</b>", esc(p.Summary))
		if d := strings.TrimSpace(p.Details); d != "" && d != strings.TrimSpace(p.Summary) {
			fmt.Fprintf(&b, "\n\n%s", esc(truncate(d, maxDetails)))
		}
		writeLink(&b, p.GoAlertURL, "Open to acknowledge or close")
	case typeAlertStatus:
		fmt.Fprintf(&b, "<b>%s alert #%d</b>", esc(app), p.AlertID)
		writeService(&b, p.ServiceName)
		if p.Summary != "" {
			fmt.Fprintf(&b, "\n%s", esc(p.Summary))
		}
		if p.LogEntry != "" {
			fmt.Fprintf(&b, "\n\n%s", esc(p.LogEntry))
		}
		writeLink(&b, p.GoAlertURL, "Open alert")
	case typeAlertBundle:
		fmt.Fprintf(&b, "<b>%s: %d unacknowledged alerts</b>", esc(app), p.Count)
		writeService(&b, p.ServiceName)
		writeLink(&b, p.GoAlertURL, "Open the service's alerts")
	case typeAlertStatusBundle:
		fmt.Fprintf(&b, "<b>%s alert #%d and %d more</b>", esc(app), p.AlertID, p.Count)
		if p.LogEntry != "" {
			fmt.Fprintf(&b, "\n\n%s", esc(p.LogEntry))
		}
		writeLink(&b, p.GoAlertURL, "Open alert")
	case typeVerification:
		fmt.Fprintf(&b, "%s verification code: <code>%s</code>", esc(app), esc(p.Code))
	case typeTest:
		fmt.Fprintf(&b, "Test message from %s: this contact method reaches you.", esc(app))
	case typeScheduleOnCall:
		fmt.Fprintf(&b, "<b>%s: on call for %s</b>", esc(app), esc(p.ScheduleName))
		names := make([]string, 0, len(p.Users))
		for _, u := range p.Users {
			names = append(names, esc(u.Name))
		}
		sort.Strings(names)
		if len(names) == 0 {
			b.WriteString("\nnobody")
		} else {
			fmt.Fprintf(&b, "\n%s", strings.Join(names, ", "))
		}
		writeLink(&b, p.ScheduleURL, "Open schedule")
	}
	return b.String()
}

func writeService(b *strings.Builder, service string) {
	if service != "" {
		fmt.Fprintf(b, "\nService: %s", esc(service))
	}
}

// writeLink adds a link only for an absolute http(s) URL: anything else in
// an href is either broken or an injection.
func writeLink(b *strings.Builder, raw, text string) {
	u, err := url.Parse(raw)
	if raw == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return
	}
	fmt.Fprintf(b, "\n\n<a href=\"%s\">%s</a>", html.EscapeString(u.String()), esc(text))
}

func esc(s string) string { return html.EscapeString(s) }

// truncate cuts s to at most n runes, marking the cut.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + " [...]"
}
