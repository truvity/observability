// karma, the alert console, and the notification links that point at it.
//
// The Slack message's `Silence:` link under `notifications.console: karma`
// is not a URL anyone writes by hand: Alertmanager executes a template that
// builds karma's own `?m=<base64 JSON>` form-prefill, and karma's UI
// decodes it with `atob` into a `SilenceFormDataFromBase64`
// (ui/src/Stores/SilenceFormStore.ts at the pinned tag). If the template
// and that type drift, the link opens an empty form, or a broken one,
// and nothing reports it. So this test does what the person's browser does:
// executes the rendered template, takes the link, decodes `m` the way
// `atob` does, and holds the JSON to karma's type.
//
// Alertmanager is not a dependency of this repository, so the template is
// executed with Go's text/template and a stub FuncMap carrying the
// Alertmanager v0.34 functions the link uses (`dict`, `list`, `append`,
// `toJson`, `base64encode`, `reReplaceAll`), written to the same
// definitions; `urlquery` is Go's own builtin, as it is in Alertmanager.
package tests

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type amPair struct{ Name, Value string }

// amKV mirrors Alertmanager's template.KV: a string map with SortedPairs.
type amKV map[string]string

func (kv amKV) SortedPairs() []amPair {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]amPair, 0, len(keys))
	for _, k := range keys {
		out = append(out, amPair{k, kv[k]})
	}
	return out
}

type amAlert struct {
	Labels      amKV
	Annotations amKV
}

type amData struct {
	Status       string
	CommonLabels amKV
	Alerts       []amAlert
}

// amFuncs is the subset of Alertmanager v0.34's template functions the
// karma links use, with the same definitions (template/template.go).
var amFuncs = template.FuncMap{
	"toJson": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	// URL-safe alphabet, as in Alertmanager.
	"base64encode": func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) },
	"reReplaceAll": func(pattern, repl, text string) string {
		return regexp.MustCompile(pattern).ReplaceAllString(text, repl)
	},
	"list": func(args ...any) ([]any, error) {
		if args == nil {
			return []any{}, nil
		}
		return args, nil
	},
	"append": func(slice []any, args ...any) []any { return append(slice, args...) },
	"dict": func(values ...any) (map[string]any, error) {
		if len(values)%2 != 0 {
			return nil, fmt.Errorf("dict requires an even number of arguments")
		}
		res := make(map[string]any, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings")
			}
			res[key] = values[i+1]
		}
		return res, nil
	},
	"toUpper": strings.ToUpper,
}

// karma's types, as written in ui/src/Stores/SilenceFormStore.ts (v0.133).
type karmaMatcher struct {
	N string   `json:"n"`
	V []string `json:"v"`
	R *bool    `json:"r"`
	E *bool    `json:"e"`
}

type karmaAM struct {
	Label string   `json:"label"`
	Value []string `json:"value"`
}

type karmaForm struct {
	AM []karmaAM      `json:"am"`
	M  []karmaMatcher `json:"m"`
	D  *int           `json:"d"`
	C  *string        `json:"c"`
}

func karmaSlackTexts(t *testing.T, golden string) []string {
	t.Helper()
	_, cfg := renderedLinksAM(t, golden)
	var texts []string
	for _, r := range cfg.Receivers {
		for _, c := range r.SlackConfigs {
			texts = append(texts, c["text"].(string))
		}
	}
	require.NotEmpty(t, texts)
	return texts
}

func executeSlackText(t *testing.T, text string, data amData) string {
	t.Helper()
	tpl, err := template.New("slack").Funcs(amFuncs).Parse(text)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, tpl.Execute(&buf, data))
	return buf.String()
}

var mrkdwnLink = regexp.MustCompile(`<([^|>]*)\|([^>]*)>`)

// slackLinks reads the one line of named mrkdwn links out of an executed
// Slack text: the names in order, and each link's URL as Slack shows it
// (Slack decodes `&amp;` back to `&`, so the test does too). It asserts
// the rule the chart exists to keep: nothing inside `<...>` but one `|`.
func slackLinks(t *testing.T, out string) ([]string, map[string]string) {
	t.Helper()
	var names []string
	urls := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "<") {
			continue
		}
		ms := mrkdwnLink.FindAllStringSubmatch(l, -1)
		require.NotEmpty(t, ms, l)
		for _, m := range ms {
			assert.NotContains(t, m[1], "<", l)
			assert.NotRegexp(t, `&(?:[^a]|a[^m]|am[^p]|amp[^;])`, m[1], "a raw & inside a link: %s", l)
			names = append(names, m[2])
			urls[m[2]] = html.UnescapeString(m[1])
		}
		assert.Equal(t, strings.Join(names, " · "), mrkdwnLinksJoined(l), "links are separated by ' · ' and nothing else is on the line")
		return names, urls
	}
	t.Fatalf("no named-link line in %q", out)
	return nil, nil
}

// mrkdwnLinksJoined is the line with every `<url|Name>` replaced by its name.
func mrkdwnLinksJoined(line string) string {
	return mrkdwnLink.ReplaceAllString(line, "$2")
}

// karmaServerName reads the name karma knows this release's Alertmanager
// by out of the rendered karma ConfigMap.
func karmaServerName(t *testing.T, golden string) string {
	t.Helper()
	for _, d := range renderedDocs(t, golden) {
		if d["kind"] != "ConfigMap" || dig(d, "data", "karma.yaml") == nil {
			continue
		}
		var cfg struct {
			Alertmanager struct {
				Servers []struct {
					Name  string `yaml:"name"`
					Proxy bool   `yaml:"proxy"`
				} `yaml:"servers"`
			} `yaml:"alertmanager"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(dig(d, "data", "karma.yaml").(string)), &cfg))
		require.NotEmpty(t, cfg.Alertmanager.Servers)
		assert.True(t, cfg.Alertmanager.Servers[0].Proxy, "silences must go through karma, or the author is not rewritten")
		return cfg.Alertmanager.Servers[0].Name
	}
	t.Fatalf("%s renders no karma ConfigMap", golden)
	return ""
}

func TestKarmaSilenceLinkOpensKarmasFormPrefilled(t *testing.T) {
	const golden = "golden/observability-stack/notifications-karma-console.yaml"
	amName := karmaServerName(t, golden)

	// Values chosen so the standard base64 of the JSON carries `+` and `/`
	// (the characters Alertmanager's URL-safe encoder writes as `-` and
	// `_`), which karma's `atob` cannot read; and one that needs escaping.
	cases := []amKV{
		{"alertname": "KubePodCrashLooping", "k8s_cluster_name": "my-cluster", "k8s_namespace_name": "payments", "severity": "critical"},
		{"alertname": "Disk???", "k8s_cluster_name": "my-cluster", "pod": "a/b c&d=e", "severity": "warning"},
		{"alertname": "X"},
	}
	var sawPlusOrSlash bool
	for _, labels := range cases {
		for _, text := range karmaSlackTexts(t, golden) {
			out := executeSlackText(t, text, amData{
				Status:       "firing",
				CommonLabels: labels,
				Alerts:       []amAlert{{Labels: labels, Annotations: amKV{"summary": "s"}}},
			})
			linkNames, links := slackLinks(t, out)
			assert.Equal(t, []string{"Silence", "View", "Grafana"}, linkNames)
			link := links["Silence"]
			require.NotEmpty(t, link, out)
			require.True(t, strings.HasPrefix(link, "https://karma.example.com/?m="), link)
			assert.NotContains(t, link, " ")

			u, err := url.Parse(link)
			require.NoError(t, err)
			m := u.Query().Get("m")
			require.NotEmpty(t, m)

			// What the browser's `atob` accepts: the standard alphabet.
			raw, err := base64.StdEncoding.DecodeString(m)
			require.NoError(t, err, "karma decodes with atob; m = %q", m)
			if strings.ContainsAny(m, "+/") {
				sawPlusOrSlash = true
			}

			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			var form karmaForm
			require.NoError(t, dec.Decode(&form), string(raw))

			require.Len(t, form.AM, 1)
			assert.Equal(t, karmaAM{Label: amName, Value: []string{amName}}, form.AM[0],
				"karma resets the form unless this equals its own option for the Alertmanager, label and value list")
			require.NotNil(t, form.D)
			assert.Equal(t, 90, *form.D)
			require.NotNil(t, form.C)
			assert.Equal(t, "", *form.C)

			got := map[string]string{}
			for _, mt := range form.M {
				require.NotNil(t, mt.R)
				require.NotNil(t, mt.E)
				assert.False(t, *mt.R, "an exact matcher, never a regex")
				assert.True(t, *mt.E)
				require.Len(t, mt.V, 1)
				got[mt.N] = mt.V[0]
			}
			assert.Equal(t, map[string]string(labels), got)
			// Matchers in sorted order, like the Alertmanager's own link.
			var names []string
			for _, mt := range form.M {
				names = append(names, mt.N)
			}
			assert.True(t, sort.StringsAreSorted(names), names)
		}
	}
	assert.True(t, sawPlusOrSlash, "no case exercised the URL-safe to standard alphabet conversion")
}

func TestKarmaViewLinkFiltersToTheAlertGroup(t *testing.T) {
	const golden = "golden/observability-stack/notifications-karma-console.yaml"
	labels := amKV{"alertname": "Disk???", "k8s_cluster_name": "my-cluster", "pod": "a b&c=d"}
	for _, text := range karmaSlackTexts(t, golden) {
		out := executeSlackText(t, text, amData{Status: "firing", CommonLabels: labels,
			Alerts: []amAlert{{Labels: labels, Annotations: amKV{"summary": "s"}}}})
		linkNames, links := slackLinks(t, out)
		assert.Equal(t, []string{"Silence", "View", "Grafana"}, linkNames)
		link := links["View"]
		assert.Contains(t, out, "&amp;q=", "Slack's escaping: & is written &amp; inside the link")
		require.True(t, strings.HasPrefix(link, "https://karma.example.com/?q="), link)
		u, err := url.Parse(link)
		require.NoError(t, err)
		assert.Equal(t, []string{"alertname=Disk???", "k8s_cluster_name=my-cluster", "pod=a b&c=d"}, u.Query()["q"], link)
		assert.Contains(t, link, "%3D", "the = between a label and its value is escaped")
		// The Grafana link is untouched.
		assert.True(t, strings.HasPrefix(links["Grafana"], "https://grafana.example/?var-cluster="), links["Grafana"])
	}
}

func TestConsoleAlertmanagerKeepsTheSilenceLink(t *testing.T) {
	// `notifications-silence-link` does not set `console`: the message is
	// today's, with no karma link and no View line. (The goldens being
	// byte-identical to master proves the rest.)
	for _, text := range karmaSlackTexts(t, "golden/observability-stack/notifications-silence-link.yaml") {
		assert.Contains(t, text, "<https://alertmanager.example/#/silences/new?filter=%7B")
		assert.Contains(t, text, "|Silence> · <")
		assert.NotContains(t, text, "|View>")
		assert.NotContains(t, text, "?m=")
	}
}

func TestKarmaOffRendersNothingOfKarma(t *testing.T) {
	goldens := []string{
		"golden/observability-stack/minimal.yaml",
		"golden/observability-stack/everything.yaml",
		"golden/observability-stack/notifications-silence-link.yaml",
	}
	for _, g := range goldens {
		for _, d := range renderedDocs(t, g) {
			assert.NotContains(t, fmt.Sprint(dig(d, "metadata", "name")), "-karma", g)
		}
	}
}

// No NetworkPolicy in this chart selects the Alertmanager pods, so karma
// reaches them already and the chart must not introduce the first policy:
// it would default-deny vmalert's alert pushes and the mesh port too.
func TestNoPolicySelectsAlertmanagerAndKarmaHasItsOwn(t *testing.T) {
	for _, g := range []string{
		"golden/observability-stack/karma.yaml",
		"golden/observability-stack/notifications-karma-console.yaml",
		"golden/observability-stack/everything.yaml",
	} {
		var karmaPolicies int
		for _, d := range renderedDocs(t, g) {
			if d["kind"] != "NetworkPolicy" {
				continue
			}
			sel := fmt.Sprint(dig(d, "spec", "podSelector", "matchLabels"))
			assert.NotContains(t, sel, "vmalertmanager", "%s: %v", g, dig(d, "metadata", "name"))
			assert.NotContains(t, fmt.Sprint(dig(d, "spec", "policyTypes")), "Egress", "%s: egress is not restricted anywhere in this chart", g)
			if strings.HasSuffix(fmt.Sprint(dig(d, "metadata", "name")), "-karma") {
				karmaPolicies++
				assert.Equal(t, "map[app.kubernetes.io/instance:observability-stack app.kubernetes.io/name:karma]", sel)
			}
		}
		if strings.Contains(g, "karma") {
			assert.Equal(t, 1, karmaPolicies, g)
		}
	}
}

// The stores admit every pod of this release carrying `part-of`; karma
// must not carry it, or it would be admitted to the stores.
func TestKarmaPodIsNotAdmittedToTheStores(t *testing.T) {
	docs := renderedDocs(t, "golden/observability-stack/karma.yaml")
	dep := findDoc(t, docs, "Deployment", func(d map[string]any) bool {
		return fmt.Sprint(dig(d, "metadata", "name")) == "observability-stack-karma"
	})
	labels := dig(dep, "spec", "template", "metadata", "labels").(map[string]any)
	assert.NotContains(t, labels, "app.kubernetes.io/part-of")
	assert.Equal(t, "karma", labels["app.kubernetes.io/name"])
}
