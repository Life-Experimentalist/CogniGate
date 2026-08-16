package conformance

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A reader for the Prometheus text exposition format, written out by hand.
//
// The obvious move is to import the official parser. This module is deliberately
// dependency-free — a third party running the suite against their own gateway
// gets `go test` and nothing to install (GW-10) — and a conformance suite that
// pulled in the reference implementation's metrics library would also be
// borrowing its idea of what the format is. What the assertions need is narrow:
// find a series by name and labels, read its value, and prove a counter rose.

// series is one sample: a metric name, its labels, and its value.
type series struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// metricsFor reports whether the sample carries every label in want, with the
// value want gives. Extra labels are ignored: the specification fixes which
// labels a series must have, not that it may have no others, and a test that
// demanded an exact set would fail a gateway for adding an instance label.
func (s series) matches(want map[string]string) bool {
	for k, v := range want {
		if s.Labels[k] != v {
			return false
		}
	}
	return true
}

// scrape is one parsed exposition response.
type scrape struct {
	samples []series
	// raw is kept for failure messages: "no such series" is only actionable
	// alongside what was actually exposed.
	raw string
}

// value sums every sample of a metric whose labels match want.
//
// Summing rather than requiring exactly one is deliberate. A counter split by a
// label the assertion does not constrain — a status code, a route — is several
// samples that together answer "how many", and a test asserting on the total
// should not have to enumerate the splits to find it. With no match the answer
// is zero, which is what a counter that has not been incremented reads as.
func (s *scrape) value(name string, want map[string]string) float64 {
	var total float64
	for _, sample := range s.samples {
		if sample.Name == name && sample.matches(want) {
			total += sample.Value
		}
	}
	return total
}

// has reports whether any sample of the metric matches, which is the question to
// ask of a gauge whose value is the subject of the next assertion — zero is a
// legitimate gauge reading and cannot stand in for absence.
func (s *scrape) has(name string, want map[string]string) bool {
	for _, sample := range s.samples {
		if sample.Name == name && sample.matches(want) {
			return true
		}
	}
	return false
}

// names lists the distinct metric names seen, for failure messages.
func (s *scrape) names() []string {
	seen := map[string]bool{}
	var out []string
	for _, sample := range s.samples {
		if !seen[sample.Name] {
			seen[sample.Name] = true
			out = append(out, sample.Name)
		}
	}
	return out
}

// parseExposition reads the Prometheus text format.
//
// It accepts what the format's grammar calls a sample line and ignores the rest:
// # HELP and # TYPE carry no assertion this suite makes, and a timestamp — the
// optional third field — is discarded rather than refused, since a gateway that
// stamps its samples is still exposing them. A line it cannot read is an error
// rather than a skip: the acceptance criterion is that the endpoint parses, so
// silently dropping the lines that do not would make it true by construction.
func parseExposition(t *testing.T, body string) *scrape {
	t.Helper()

	out := &scrape{raw: body}
	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sample, err := parseSample(line)
		if err != nil {
			t.Fatalf("/metrics does not parse as Prometheus text: line %d: %v\n%q", i+1, err, line)
		}
		out.samples = append(out.samples, sample)
	}
	return out
}

func parseSample(line string) (series, error) {
	name, rest := line, ""

	if open := strings.IndexByte(line, '{'); open >= 0 {
		close := strings.LastIndexByte(line, '}')
		if close < open {
			return series{}, fmt.Errorf("unclosed label set")
		}
		name = line[:open]
		labels, err := parseLabels(line[open+1 : close])
		if err != nil {
			return series{}, err
		}
		value, err := parseValue(line[close+1:])
		if err != nil {
			return series{}, err
		}
		return series{Name: strings.TrimSpace(name), Labels: labels, Value: value}, nil
	}

	// No label set: the name and the value are separated by whitespace.
	space := strings.IndexAny(line, " \t")
	if space < 0 {
		return series{}, fmt.Errorf("no value")
	}
	name, rest = line[:space], line[space:]
	value, err := parseValue(rest)
	if err != nil {
		return series{}, err
	}
	return series{Name: name, Labels: map[string]string{}, Value: value}, nil
}

// parseValue takes the value field and drops an optional timestamp after it.
func parseValue(rest string) (float64, error) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, fmt.Errorf("no value")
	}
	// The format spells these three as words rather than as numbers.
	switch fields[0] {
	case "NaN", "+Inf", "-Inf":
		return 0, nil
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("value %q is not a number", fields[0])
	}
	return v, nil
}

// parseLabels reads the comma-separated name="value" list between the braces.
//
// Values are scanned character by character rather than split on commas, because
// a label value may legitimately contain one — and the escapes the format
// defines (\\, \" and \n) are undone here so a comparison against a plain Go
// string works.
func parseLabels(raw string) (map[string]string, error) {
	labels := map[string]string{}
	for i := 0; i < len(raw); {
		// A trailing comma before the closing brace is permitted.
		for i < len(raw) && (raw[i] == ',' || raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) {
			break
		}

		eq := strings.IndexByte(raw[i:], '=')
		if eq < 0 {
			return nil, fmt.Errorf("label %q has no value", raw[i:])
		}
		key := strings.TrimSpace(raw[i : i+eq])
		i += eq + 1
		if i >= len(raw) || raw[i] != '"' {
			return nil, fmt.Errorf("label %q is not quoted", key)
		}
		i++

		var value strings.Builder
		closed := false
		for i < len(raw) {
			switch raw[i] {
			case '\\':
				if i+1 >= len(raw) {
					return nil, fmt.Errorf("label %q ends in a backslash", key)
				}
				switch raw[i+1] {
				case 'n':
					value.WriteByte('\n')
				default:
					value.WriteByte(raw[i+1])
				}
				i += 2
			case '"':
				i++
				closed = true
			default:
				value.WriteByte(raw[i])
				i++
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, fmt.Errorf("label %q is unterminated", key)
		}
		labels[key] = value.String()
	}
	return labels, nil
}

// --- scraping ---------------------------------------------------------------

// metrics fetches and parses the gateway's exposition endpoint.
//
// GW-8 puts it on the main listener by default and leaves it unauthenticated,
// so the ordinary case sends no credential. CONF_METRICS_TOKEN covers the
// deployment that chose to guard it.
func (c *client) metrics(t *testing.T) *scrape {
	t.Helper()

	resp := c.do(t, http.MethodGet, "/metrics", suite.cfg.MetricsToken, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /metrics: status %d, want 200\n%s", resp.Status, truncate(resp.Body))
	}
	return parseExposition(t, string(resp.Body))
}
