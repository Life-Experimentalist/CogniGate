package conformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- acceptance-criterion identity ------------------------------------------
//
// A test's name is the only place its acceptance criterion is written down.
// Deriving the id from the name rather than passing it as an argument removes
// the failure mode where the two disagree and the coverage script believes the
// argument while a reader believes the name.

var acPattern = regexp.MustCompile(`^Test_?GW(\d+)_AC(\d+)_`)

// acID turns TestGW3_AC2_FallbackOnServerError into "GW-3.AC-2".
func acID(testName string) string {
	m := acPattern.FindStringSubmatch(testName)
	if m == nil {
		return ""
	}
	req, _ := strconv.Atoi(m[1])
	ac, _ := strconv.Atoi(m[2])
	return fmt.Sprintf("GW-%d.AC-%d", req, ac)
}

// requirementOf returns the "GW-3" part of an id.
func requirementOf(id string) string {
	if i := strings.IndexByte(id, '.'); i > 0 {
		return id[:i]
	}
	return id
}

// --- capability gating ------------------------------------------------------

// capabilityExempt names the requirements that are never gated on what the
// target claims, because a running gateway has nothing to claim about them:
// GW-10 is this suite, and GW-11 is a deployment's resource footprint. Both are
// observed from outside the process rather than implemented inside it, so
// /v1/meta will never list them and their sections must run regardless.
//
// Everything else is gated. GW-9 makes `capabilities` the target's own account
// of what it implements, so the suite takes it at its word in both directions:
// a claimed requirement is tested and must pass, and an unclaimed one is skipped
// rather than failed.
var capabilityExempt = map[string]bool{
	"GW-10": true,
	"GW-11": true,
}

// gatedOut reports whether the target's capability list excuses a requirement's
// section from running. The argument is a "GW-3"-style id.
func gatedOut(requirement string) bool {
	if capabilityExempt[requirement] {
		return false
	}
	return !suite.capabilities[capabilityID(requirement)]
}

// capabilityID turns "GW-3" into the "gw-3" that /v1/meta publishes.
func capabilityID(requirement string) string { return strings.ToLower(requirement) }

// begin registers the test for the report, skips it when the target does not
// claim the capability it exercises, and hands back the client.
//
// Every conformance test starts with this call.
func begin(t *testing.T) *client {
	t.Helper()

	id := acID(t.Name())
	if id == "" {
		t.Fatalf("test name %q does not embed an acceptance-criterion id; "+
			"conformance tests must be named TestGW<n>_AC<n>_<Description>", t.Name())
	}
	record(t, id)

	if suite.cfg.BaseURL == "" {
		t.Skip("no target: set CONF_BASE_URL to the gateway under test")
	}
	if suite.provision != nil {
		t.Fatalf("the suite could not provision against the target: %v", suite.provision)
	}
	if requirement := requirementOf(id); gatedOut(requirement) {
		t.Skipf("not claimed: /v1/meta capabilities do not include %q", capabilityID(requirement))
	}
	return suite.client
}

// beginOffline registers a test that asserts a property of the repository
// rather than of a deployment, and so has no target to skip for.
//
// GW-9.AC-6 is the only one of these: it is a release-process rule, checked in
// CI against the changelog, because no running gateway can answer whether its
// release notes said the right thing.
func beginOffline(t *testing.T) {
	t.Helper()

	id := acID(t.Name())
	if id == "" {
		t.Fatalf("test name %q does not embed an acceptance-criterion id; "+
			"conformance tests must be named TestGW<n>_AC<n>_<Description>", t.Name())
	}
	record(t, id)
}

// requireFeature skips a single test whose capability is narrower than its
// requirement's — GW-5 is mostly unconditional, for instance, but its webhook
// delivery is not.
func requireFeature(t *testing.T, feature string) {
	t.Helper()
	if !suite.features[feature] {
		t.Skipf("not claimed: /v1/meta does not advertise %q", feature)
	}
}

// --- results ----------------------------------------------------------------

type result struct {
	ID         string `json:"id"`
	Test       string `json:"test"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

var (
	resultsMu sync.Mutex
	results   []result
)

// record attaches an outcome to the report when the test finishes. The cleanup
// runs after the test body has returned, so t.Failed and t.Skipped are settled
// by the time it reads them.
func record(t *testing.T, id string) {
	t.Helper()
	started := time.Now()

	t.Cleanup(func() {
		status := "pass"
		switch {
		case t.Failed():
			status = "fail"
		case t.Skipped():
			status = "skip"
		}

		resultsMu.Lock()
		results = append(results, result{
			ID:         id,
			Test:       t.Name(),
			Status:     status,
			DurationMS: time.Since(started).Milliseconds(),
		})
		resultsMu.Unlock()
	})
}

type reportDocument struct {
	Target         string         `json:"target"`
	GatewayVersion string         `json:"gateway_version"`
	SuiteVersion   string         `json:"suite_version"`
	Results        []result       `json:"results"`
	Summary        map[string]int `json:"summary"`
}

// writeReport emits conformance-report.json. A failure to write it is reported
// but does not change the exit status: the test results are the verdict, and
// losing the report should not turn a clean run red or a dirty one green.
func writeReport() {
	resultsMu.Lock()
	defer resultsMu.Unlock()

	sort.Slice(results, func(i, j int) bool { return results[i].Test < results[j].Test })
	if results == nil {
		// The spec calls this field results[]. A nil slice encodes as null, which
		// a consumer doing `for r in report["results"]` has to special-case for no
		// reason.
		results = []result{}
	}

	summary := map[string]int{"pass": 0, "fail": 0, "skip": 0}
	for _, r := range results {
		summary[r.Status]++
	}

	path := os.Getenv("CONF_REPORT")
	if path == "" {
		path = "conformance-report.json"
	}

	doc := reportDocument{
		Target:         suite.cfg.BaseURL,
		GatewayVersion: suite.version,
		SuiteVersion:   SuiteVersion,
		Results:        results,
		Summary:        summary,
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "conformance: could not encode the report: %v\n", err)
		return
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "conformance: could not write %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(os.Stderr, "conformance: %d pass, %d fail, %d skip → %s\n",
		summary["pass"], summary["fail"], summary["skip"], path)
}

// --- entry point ------------------------------------------------------------

func TestMain(m *testing.M) {
	suite.cfg = loadConfig()

	if suite.cfg.BaseURL == "" {
		// Every test skips, and the report still lists the full acceptance
		// inventory. That is a more useful artefact than refusing to run, and
		// it keeps `go test ./...` at the repository root honest on a machine
		// with no gateway on it.
		fmt.Fprintln(os.Stderr,
			"conformance: CONF_BASE_URL is not set; skipping every test. "+
				"Point it at a running gateway to run the suite.")
		code := m.Run()
		writeReport()
		os.Exit(code)
	}

	suite.client = &client{
		base: suite.cfg.BaseURL,
		// Above the gateway's own 120s request budget, so a test observes the
		// gateway's timeout behaviour rather than this client's.
		http: &http.Client{Timeout: 150 * time.Second},
	}

	suite.runID = strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
	suite.provision = provision(suite.runID)
	if suite.provision == nil {
		suite.provision = loadCapabilities()
	}
	if suite.provision != nil {
		fmt.Fprintf(os.Stderr, "conformance: setup failed: %v\n", suite.provision)
	}

	code := m.Run()

	if err := checkCapabilityClaims(); err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		if code == 0 {
			code = 1
		}
	}

	if err := deprovision(); err != nil {
		fmt.Fprintf(os.Stderr, "conformance: teardown failed: %v\n", err)
		if code == 0 {
			// GW-10.AC-4 is a property of the run, not of any one test: a suite
			// that leaves a tenant behind has not passed, however green its
			// assertions were.
			code = 1
		}
	}

	writeReport()
	os.Exit(code)
}

// loadCapabilities reads what the target claims, which is what decides between
// "skip" and "fail" for every optional requirement (GW-10.AC-5).
func loadCapabilities() error {
	resp, err := suite.client.try(http.MethodGet, "/v1/meta", suite.dataKey, nil)
	if err != nil {
		return fmt.Errorf("reading /v1/meta: %w", err)
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("reading /v1/meta: status %d\n%s", resp.Status, truncate(resp.Body))
	}

	var meta struct {
		Version      string          `json:"version"`
		Capabilities []string        `json:"capabilities"`
		Features     map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(resp.Body, &meta); err != nil {
		return fmt.Errorf("parsing /v1/meta: %w", err)
	}

	suite.version = meta.Version
	suite.capabilities = map[string]bool{}
	for _, id := range meta.Capabilities {
		suite.capabilities[strings.ToLower(strings.TrimSpace(id))] = true
	}
	suite.features = meta.Features
	if suite.features == nil {
		suite.features = map[string]bool{}
	}
	return nil
}

// checkCapabilityClaims enforces the half of GW-9.AC-2 that no single test can
// see, because it is a property of the whole run rather than of any one call:
// every requirement the target claims must have been exercised and come back
// clean, and every requirement it does not claim must have been skipped rather
// than failed.
//
// The second half is what makes the first safe to enforce. A suite that quietly
// failed the sections it had gated off would make the capability list useless as
// a selector — and a suite that reported "pass" for a claimed requirement it
// never ran would make it useless as a promise.
func checkCapabilityClaims() error {
	resultsMu.Lock()
	defer resultsMu.Unlock()

	// Per requirement: how many results, and how many of each status.
	type tally struct{ pass, fail, skip int }
	byRequirement := map[string]*tally{}
	for _, r := range results {
		req := requirementOf(r.ID)
		t, ok := byRequirement[req]
		if !ok {
			t = &tally{}
			byRequirement[req] = t
		}
		switch r.Status {
		case "pass":
			t.pass++
		case "fail":
			t.fail++
		default:
			t.skip++
		}
	}

	var problems []string
	for req, t := range byRequirement {
		if capabilityExempt[req] {
			continue
		}
		claimed := suite.capabilities[capabilityID(req)]
		switch {
		case claimed && t.pass == 0:
			// Every criterion skipped, or none ran. Either way the claim went
			// unverified, which GW-9 does not allow a claim to do.
			problems = append(problems, fmt.Sprintf(
				"%s is claimed by /v1/meta but nothing in its section passed (%d skipped, %d failed)", req, t.skip, t.fail))
		case !claimed && t.fail > 0:
			problems = append(problems, fmt.Sprintf(
				"%s is not claimed by /v1/meta but %d of its criteria failed; an unclaimed requirement must skip", req, t.fail))
		}
	}

	// A claim with no section at all is the same unverified promise, and does
	// not appear in the loop above because it produced no results.
	for id := range suite.capabilities {
		req := strings.ToUpper(id)
		if byRequirement[req] == nil {
			problems = append(problems, fmt.Sprintf("%s is claimed by /v1/meta but the suite has no section for it", req))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("capability claims do not match the run (GW-9.AC-2):\n  %s", strings.Join(problems, "\n  "))
}
