package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatEveryType(t *testing.T) {
	cases := []struct {
		name string
		p    Payload
		want []string
		not  []string
	}{
		{
			name: "alert",
			p: Payload{
				Type: typeAlert, AlertID: 7, Summary: "Disk <full>", Details: "Disk <full>",
				ServiceName: "Ops", GoAlertURL: "https://oncall.example/alerts/7",
			},
			want: []string{
				"<b>GoAlert alert #7</b>", "Service: Ops", "<b>Disk &lt;full&gt;</b>",
				`<a href="https://oncall.example/alerts/7">Open to acknowledge or close</a>`,
			},
			// Details equal to the summary are not repeated.
			not: []string{"Disk &lt;full&gt;</b>\n\nDisk"},
		},
		{
			name: "status",
			p:    Payload{Type: typeAlertStatus, AlertID: 7, Summary: "s", LogEntry: "Acknowledged by Alice", GoAlertURL: "https://oncall.example/alerts/7"},
			want: []string{"alert #7", "Acknowledged by Alice", "Open alert"},
		},
		{
			name: "bundle",
			p:    Payload{Type: typeAlertBundle, Count: 3, ServiceName: "Ops", GoAlertURL: "https://oncall.example/services/x/alerts"},
			want: []string{"3 unacknowledged alerts", "Service: Ops"},
		},
		{
			name: "status bundle",
			p:    Payload{Type: typeAlertStatusBundle, AlertID: 9, Count: 2, LogEntry: "Closed"},
			want: []string{"alert #9 and 2 more", "Closed"},
		},
		{
			name: "verification",
			p:    Payload{Type: typeVerification, AppName: "On call", Code: "123456"},
			want: []string{"On call verification code: <code>123456</code>"},
		},
		{
			name: "test",
			p:    Payload{Type: typeTest},
			want: []string{"Test message from GoAlert"},
		},
		{
			name: "on call",
			p: Payload{
				Type: typeScheduleOnCall, ScheduleName: "Primary",
				Users: []OnCallUser{{Name: "Bob"}, {Name: "Alice"}}, ScheduleURL: "https://oncall.example/schedules/1",
			},
			want: []string{"on call for Primary", "Alice, Bob", "Open schedule"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Format(c.p)
			for _, w := range c.want {
				assert.Contains(t, got, w)
			}
			for _, n := range c.not {
				assert.NotContains(t, got, n)
			}
		})
	}
}

func TestFormatRefusesNonHTTPLinks(t *testing.T) {
	for _, u := range []string{"javascript:alert(1)", "/alerts/1", "", "ftp://x/y"} {
		got := Format(Payload{Type: typeAlert, AlertID: 1, Summary: "s", GoAlertURL: u})
		assert.NotContains(t, got, "<a ", u)
	}
	got := Format(Payload{Type: typeAlert, AlertID: 1, Summary: "s", GoAlertURL: `https://x.example/a?b="c"`})
	// Telegram reads numeric entities: the quote cannot end the attribute.
	assert.Contains(t, got, `href="https://x.example/a?b=&#34;c&#34;"`)
}

func TestFormatTruncatesLongDetails(t *testing.T) {
	got := Format(Payload{Type: typeAlert, AlertID: 1, Summary: "s", Details: strings.Repeat("é", 10000)})
	assert.Less(t, utf8.RuneCountInString(got), 4096)
	assert.Contains(t, got, "[...]")
}

func TestDecodePayload(t *testing.T) {
	p, err := DecodePayload([]byte(`{"Type":"Alert","AlertID":5,"Summary":"x","Meta":{"k":"v"}}`))
	require.NoError(t, err)
	assert.Equal(t, 5, p.AlertID)
	assert.Equal(t, "v", p.Meta["k"])

	_, err = DecodePayload([]byte(`not json`))
	assert.Error(t, err)
	_, err = DecodePayload([]byte(`{"Type":"Nope"}`))
	assert.ErrorContains(t, err, "unsupported")
	assert.Equal(t, "unknown", typeLabel("Nope"))
	assert.Equal(t, "Alert", typeLabel("Alert"))
}
