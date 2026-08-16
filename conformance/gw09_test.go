package conformance

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// GW-9 — versioning & compatibility.
//
// The endpoint under test is the one the rest of the suite depends on: /v1/meta
// is where a deployment says what it implements, and the suite decides which
// sections to run from that answer. So these criteria are partly about the
// document's shape and partly about whether the claim it makes is honest —
// whether the limits it publishes are the ones enforced, and whether a
// capability it leaves out is genuinely absent from the wire.

// semverPattern is the expression published at semver.org, anchored. It is
// written out rather than imported because this module is deliberately
// stdlib-only: a conformance suite that pulls in dependencies is one more thing
// that can differ between the machine that certifies a release and the machine
// that ships it.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// capabilityPattern matches the ids GW-9 fixes: gw-1 through gw-14, lower case,
// no zero padding.
var capabilityPattern = regexp.MustCompile(`^gw-(?:[1-9]|1[0-4])$`)

// allRequirements is every requirement the specification defines, in the
// "GW-3" spelling the report uses.
var allRequirements = func() []string {
	out := make([]string, 0, 14)
	for i := 1; i <= 14; i++ {
		out = append(out, fmt.Sprintf("GW-%d", i))
	}
	return out
}()

func TestGW9_AC1_MetaReportsVersionAndCapabilities(t *testing.T) {
	c := begin(t)

	resp := c.data(t, http.MethodGet, "/v1/meta", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/meta with a data key answered %d, want 200\n%s", resp.Status, truncate(resp.Body))
	}

	body := resp.JSON(t)

	version, _ := body["version"].(string)
	if !semverPattern.MatchString(version) {
		t.Errorf("version is %q, which is not semver; GW-9 publishes it as a value a client compares", version)
	}
	if got, _ := body["api_version"].(string); got != "v1" {
		t.Errorf("api_version is %q, want %q — it tracks the major, and these routes are /v1/*", got, "v1")
	}
	if got, _ := body["name"].(string); got == "" {
		t.Error("name is empty; the document has to say what it is describing")
	}

	raw, ok := body["capabilities"].([]any)
	if !ok {
		t.Fatalf("capabilities is %T, want an array of gw-N ids", body["capabilities"])
	}
	if len(raw) == 0 {
		t.Fatal("capabilities is empty; a deployment claiming nothing could not have answered this call either")
	}
	seen := map[string]bool{}
	for _, entry := range raw {
		id, ok := entry.(string)
		if !ok {
			t.Errorf("capability %v is %T, want a string", entry, entry)
			continue
		}
		if !capabilityPattern.MatchString(id) {
			t.Errorf("capability %q is not a gw-N id in GW-1..GW-14", id)
		}
		if seen[id] {
			t.Errorf("capability %q is listed twice", id)
		}
		seen[id] = true
	}

	// GW-9 is being exercised right now, so the target had better claim it.
	// This is the smallest possible case of the claim being true, and it is the
	// one the whole suite's gating rests on.
	if !seen["gw-9"] {
		t.Errorf("gw-9 is not among %v, yet /v1/meta answered — a deployment that serves this endpoint implements the requirement that defines it", raw)
	}
}

func TestGW9_AC2_CapabilitiesSelectTheSuite(t *testing.T) {
	begin(t)

	// The run-level half of this criterion — that every claimed requirement was
	// actually exercised and passed, and that no unclaimed one failed — is
	// enforced in TestMain, because no test can see the whole run's results
	// from inside it. What is checkable here is the mechanism that produces
	// that outcome: which requirements the suite will gate off, and why.
	for _, requirement := range allRequirements {
		claimed := suite.capabilities[capabilityID(requirement)]
		exempt := capabilityExempt[requirement]

		want := !exempt && !claimed
		if got := gatedOut(requirement); got != want {
			t.Errorf("%s: gatedOut is %v, want %v (claimed=%v, exempt=%v)", requirement, got, want, claimed, exempt)
		}
	}

	// GW-10 is this suite and GW-11 is a deployment's resource footprint.
	// Neither is a behaviour the process implements, so a target that claims
	// one has misunderstood the list — and the suite would then be gating its
	// own sections on an answer from the thing under test.
	for _, id := range []string{"gw-10", "gw-11"} {
		if suite.capabilities[id] {
			t.Errorf("/v1/meta claims %q; it names something observed from outside the process, not implemented inside it", id)
		}
		if !capabilityExempt[strings.ToUpper(id)] {
			t.Errorf("%s is not exempt from gating; its section would be skipped on a target that cannot claim it", strings.ToUpper(id))
		}
	}
}

func TestGW9_AC3_PublishedLimitsAreTheEnforcedOnes(t *testing.T) {
	c := begin(t)

	body := c.data(t, http.MethodGet, "/v1/meta", nil).JSON(t)
	limits, ok := body["limits"].(map[string]any)
	if !ok {
		t.Fatalf("limits is %T, want an object", body["limits"])
	}
	maxBytes, ok := limits["max_request_bytes"].(float64)
	if !ok || maxBytes < 1 {
		t.Fatalf("max_request_bytes is %v, want a positive number", limits["max_request_bytes"])
	}
	limit := int(maxBytes)

	// A published limit nobody enforces is worse than no limit at all: a client
	// sizes its batches against it. So the criterion is not that the field
	// exists but that it is the figure the gateway actually rejects at.
	if resp := c.do(t, http.MethodPost, "/v1/chat/completions", suite.dataKey, paddedRequest(limit+1)); resp.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("a request of max_request_bytes+1 (%d bytes) answered %d, want 413\n%s", limit+1, resp.Status, truncate(resp.Body))
	} else if code := resp.ErrorCode(t); code != "request_too_large" {
		t.Errorf("the oversize request was rejected with %q, want request_too_large", code)
	}

	// And the other side of the boundary: a request of exactly the published
	// size must fail for some reason of its own, or none, but never for being
	// too large. Without this the criterion would pass on a gateway whose real
	// limit was a tenth of what it advertised.
	if resp := c.do(t, http.MethodPost, "/v1/chat/completions", suite.dataKey, paddedRequest(limit)); resp.Status == http.StatusRequestEntityTooLarge {
		t.Errorf("a request of exactly max_request_bytes (%d) was rejected as too large; the enforced limit is below the published one", limit)
	}
}

func TestGW9_AC4_UnclaimedCapabilitiesAreAbsentFromTheWire(t *testing.T) {
	c := begin(t)

	// Response caching is the specification's own example of an optional
	// capability, and the one whose absence is observable: GW-12 answers with
	// X-CogniGate-Cache, so a deployment that does not claim it must never send
	// that header.
	if suite.capabilities["gw-12"] {
		t.Skip("gw-12 is claimed; this criterion is about a capability that is off")
	}

	probes := map[string]*response{
		"GET /v1/meta":              c.data(t, http.MethodGet, "/v1/meta", nil),
		"GET /v1/models":            c.data(t, http.MethodGet, "/v1/models", nil),
		"POST /v1/chat/completions": chat(t, suite.dataKey, "mock-chat-a"),
	}
	for route, resp := range probes {
		if got := resp.Header.Get("X-CogniGate-Cache"); got != "" {
			t.Errorf("%s answered with X-CogniGate-Cache: %q, but gw-12 is not claimed", route, got)
		}
	}

	// The same request twice. A cache that were quietly on would answer the
	// second one from memory, and the header is how a client is told — so its
	// absence on a repeat is the stronger statement.
	if resp := chat(t, suite.dataKey, "mock-chat-a"); resp.Header.Get("X-CogniGate-Cache") != "" {
		t.Errorf("a repeated completion answered with X-CogniGate-Cache: %q", resp.Header.Get("X-CogniGate-Cache"))
	}
}

func TestGW9_AC5_MetaIsCheapEnoughToPoll(t *testing.T) {
	c := begin(t)

	// GW-9 requires the document to be served from memory. The observable form
	// of that is latency: 100 calls, p99 under 50 ms. The measurement includes
	// this client's own round trip, which makes it a ceiling on the gateway's
	// share rather than a reading of it — a pass is unambiguous, and a failure
	// on a loaded machine is worth looking at either way.
	const calls = 100
	samples := make([]time.Duration, 0, calls)
	for i := 0; i < calls; i++ {
		started := time.Now()
		resp, err := c.try(http.MethodGet, "/v1/meta", suite.dataKey, nil)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if resp.Status != http.StatusOK {
			t.Fatalf("call %d answered %d, want 200", i+1, resp.Status)
		}
		samples = append(samples, elapsed)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	// Nearest-rank: the smallest sample at or above the 99th percentile.
	p99 := samples[int(math.Ceil(0.99*float64(len(samples))))-1]
	if p99 >= 50*time.Millisecond {
		t.Errorf("/v1/meta p99 is %v over %d calls, want under 50ms; GW-9 requires it served from memory", p99, calls)
	}
}

func TestGW9_AC6_DeprecationsAreNamedInTheChangelog(t *testing.T) {
	beginOffline(t)

	// A release-process rule rather than a runtime one: no gateway can answer
	// whether its own release notes said the right thing. GW-9 asserts it in CI
	// instead, against the repository the release is cut from.
	path, err := findChangelog()
	if err != nil {
		t.Skipf("no CHANGELOG.md above %s: this criterion is checked in the repository, not against a deployment", workingDir())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	releases := splitReleases(string(raw))
	if len(releases) == 0 {
		t.Fatalf("%s has no release sections; the check would pass vacuously on an empty file", path)
	}

	// A release that deprecates something owes the reader two things: the name
	// of what is going away, and the date it stops working. Without the date a
	// client cannot plan, and the six-month clock GW-9 promises cannot start.
	sunset := regexp.MustCompile(`sunset=(\d{4}-\d{2}-\d{2})`)
	for _, r := range releases {
		if !strings.Contains(strings.ToLower(r.body), "deprecat") {
			continue
		}
		if !sunset.MatchString(r.body) {
			t.Errorf("release %q announces a deprecation but names no sunset date; GW-9 requires the X-CogniGate-Deprecation element and its sunset=<RFC 3339 date>", r.heading)
			continue
		}
		for _, m := range sunset.FindAllStringSubmatch(r.body, -1) {
			if _, err := time.Parse("2006-01-02", m[1]); err != nil {
				t.Errorf("release %q has sunset=%s, which is not an RFC 3339 date", r.heading, m[1])
			}
		}
	}
}

// --- helpers ----------------------------------------------------------------

// paddedRequest builds a syntactically valid chat request of exactly n bytes, so
// a size limit can be probed on both sides of its boundary.
//
// The padding is a run of one character. GW-14 forbids the gateway from storing
// or logging request content, and a test that sent something resembling a prompt
// would make a leak harder to spot rather than easier.
func paddedRequest(n int) json.RawMessage {
	const shell = `{"model":"conformance-oversize","messages":[{"role":"user","content":"%s"}]}`
	pad := n - len(fmt.Sprintf(shell, ""))
	if pad < 0 {
		pad = 0
	}
	return json.RawMessage(fmt.Sprintf(shell, strings.Repeat("A", pad)))
}

// release is one section of the changelog: its heading and everything up to the
// next heading of the same level.
type release struct {
	heading string
	body    string
}

var releaseHeading = regexp.MustCompile(`(?m)^##\s+(.+)$`)

func splitReleases(text string) []release {
	locs := releaseHeading.FindAllStringSubmatchIndex(text, -1)
	out := make([]release, 0, len(locs))
	for i, loc := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, release{
			heading: strings.TrimSpace(text[loc[2]:loc[3]]),
			body:    text[loc[1]:end],
		})
	}
	return out
}

// findChangelog walks up from the working directory. The suite runs from
// conformance/ during development and from an arbitrary directory in a
// container, so the file is looked for rather than assumed.
func findChangelog() (string, error) {
	dir := workingDir()
	for {
		candidate := filepath.Join(dir, "CHANGELOG.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no CHANGELOG.md found")
		}
		dir = parent
	}
}

func workingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
