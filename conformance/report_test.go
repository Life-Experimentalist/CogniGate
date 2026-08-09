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

// featureGate maps a requirement onto the /v1/meta feature flag that decides
// whether the target claims it.
//
// The gateway advertises capabilities by feature name rather than by
// requirement number, so this translation has to live somewhere; putting it in
// the suite keeps the gateway from having to grow a vocabulary that exists only
// for its own test suite. A requirement absent from this map is unconditional:
// there is no version of CogniGate that may decline to implement its error
// envelope or its admin API.
var featureGate = map[string]string{
	"GW-2":  "aliases",
	"GW-3":  "fallback_chains",
	"GW-4":  "quotas",
	"GW-12": "response_cache",
}

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
	if feature := featureGate[requirementOf(id)]; feature != "" && !suite.features[feature] {
		t.Skipf("not claimed: /v1/meta does not advertise %q", feature)
	}
	return suite.client
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

	runID := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
	suite.provision = provision(runID)
	if suite.provision == nil {
		suite.provision = loadCapabilities()
	}
	if suite.provision != nil {
		fmt.Fprintf(os.Stderr, "conformance: setup failed: %v\n", suite.provision)
	}

	code := m.Run()

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
		Version  string          `json:"version"`
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(resp.Body, &meta); err != nil {
		return fmt.Errorf("parsing /v1/meta: %w", err)
	}

	suite.version = meta.Version
	suite.features = meta.Features
	if suite.features == nil {
		suite.features = map[string]bool{}
	}
	return nil
}
