package conformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// GW-10 — the criteria the conformance suite asserts about itself.
//
// Every other file here asserts something about a running gateway. This one
// asserts things about this repository: that the suite covers what the
// specifications define, that it cleans up after itself, and that CI runs it the
// way GW-10 says it must be run.
//
// Four of the six criteria are properties of a CI run rather than of anything a
// test can perform. "The suite passes against the reference deployment" is the
// clearest of them: a test inside that suite cannot assert it without asserting
// its own result. What those tests assert instead is that CI is *wired* to check
// the property — so deleting the job turns this file red rather than turning the
// requirement silent. The enforcement is the workflow's exit code; the test is
// the tripwire on the workflow.
//
// GW-10 is capability-exempt (see capabilityExempt): a gateway has nothing to
// claim about the suite that tests it, so /v1/meta will never list `gw-10` and
// these criteria run whenever they can rather than when they are claimed.

// --- locating the repository ------------------------------------------------

// sourceRoot walks up from the working directory to the directory holding both
// spec/ and go.mod. `go test` runs in the package's own directory, so the root
// is one level up for a local run and wherever the image put it in a container.
//
// Not gw01_test.go's repoRoot, which looks for a .git directory. That one wants
// a checkout, because GW-1.AC-7 greps the deployment's committed configuration
// and there is nothing to grep without one. These criteria want the sources the
// suite was built from, which the container image carries without the history.
func sourceRoot(t *testing.T) string {
	t.Helper()

	start := workingDir()
	dir := start
	for {
		_, specErr := os.Stat(filepath.Join(dir, "spec"))
		_, modErr := os.Stat(filepath.Join(dir, "go.mod"))
		if specErr == nil && modErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no repository root above %s: looked for a directory holding both spec/ and go.mod. "+
				"The criteria in this file read the repository, so they need it on disk; "+
				"a container image that runs the suite must copy spec/ and the workflows in alongside the tests", start)
		}
		dir = parent
	}
}

// repoFile reads a path relative to the repository root, failing the test if it
// is missing. Absence is the interesting outcome for most of these criteria —
// the file going away is exactly the regression they exist to catch — so it is
// reported as a failure rather than returned as an error for a caller to handle.
func repoFile(t *testing.T, rel ...string) string {
	t.Helper()

	path := filepath.Join(append([]string{sourceRoot(t)}, rel...)...)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.ToSlash(filepath.Join(rel...)), err)
	}
	return string(body)
}

// --- reading the specifications ---------------------------------------------

// acDefinition matches an acceptance criterion where it is *defined*: a list
// item whose first content is the bolded id.
//
// The anchoring is the whole point. Specifications cross-reference each other's
// criteria in prose — there are eleven such references today, "as GW-3.AC-2
// requires" and the like — and a pattern that matched a bare `GW-3.AC-2`
// anywhere would count every one of them as a criterion the *referring* file had
// invented. The coverage check would then demand tests for criteria that do not
// exist, and the number it reports would be a number nobody could reconcile
// against the specifications.
var acDefinition = regexp.MustCompile(`(?m)^- \*\*GW-(\d+)\.AC-(\d+)\*\*`)

// specFileNumber pulls the requirement number out of `gw-03-fallback-chains.md`.
var specFileNumber = regexp.MustCompile(`^gw-(\d+)-`)

// specCriteria returns every acceptance criterion the specifications define,
// mapped to the file that defines it.
func specCriteria(t *testing.T) map[string]string {
	t.Helper()

	pattern := filepath.Join(sourceRoot(t), "spec", "gw-*.md")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("globbing %s: %v", pattern, err)
	}

	defined := map[string]string{}
	for _, path := range paths {
		name := filepath.Base(path)
		numbered := specFileNumber.FindStringSubmatch(name)
		if numbered == nil {
			t.Errorf("spec/%s is not named gw-<number>-<slug>.md, so the criteria in it cannot be "+
				"attributed to a requirement", name)
			continue
		}
		fileNumber, _ := strconv.Atoi(numbered[1])

		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading spec/%s: %v", name, err)
		}

		for _, match := range acDefinition.FindAllStringSubmatch(string(body), -1) {
			requirement, _ := strconv.Atoi(match[1])
			criterion, _ := strconv.Atoi(match[2])
			if requirement != fileNumber {
				t.Errorf("spec/%s defines GW-%d.AC-%d, which belongs to GW-%d. "+
					"A criterion is defined in its own specification and referenced from every other; "+
					"a reference is written in prose, not as a bolded list item",
					name, requirement, criterion, fileNumber)
				continue
			}
			id := fmt.Sprintf("GW-%d.AC-%d", requirement, criterion)
			if prior, seen := defined[id]; seen {
				t.Errorf("%s is defined twice, in spec/%s and spec/%s", id, prior, name)
				continue
			}
			defined[id] = name
		}
	}

	if len(defined) == 0 {
		t.Fatalf("no acceptance criteria found under spec/ matching %q — either the directory is empty "+
			"or the specifications stopped writing criteria as `- **GW-n.AC-n**` list items, "+
			"which is the form this check reads", acDefinition)
	}
	return defined
}

// --- reading the suite ------------------------------------------------------

// testDefinition matches the declaration of a conformance test. The name is
// where a test's criterion is written down (see acID), so the declaration is
// also the coverage record.
var testDefinition = regexp.MustCompile(`(?m)^func (Test_?GW\d+_AC\d+_\w*)\(`)

// suiteTests returns every acceptance criterion the suite covers, mapped to the
// tests that claim it. The value is a slice because two tests claiming one
// criterion is a finding, not something to silently collapse.
func suiteTests(t *testing.T) map[string][]string {
	t.Helper()

	pattern := filepath.Join(sourceRoot(t), "conformance", "*_test.go")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("globbing %s: %v", pattern, err)
	}

	covered := map[string][]string{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", filepath.Base(path), err)
		}
		for _, match := range testDefinition.FindAllStringSubmatch(string(body), -1) {
			name := match[1]
			id := acID(name)
			if id == "" {
				// Unreachable while testDefinition and acPattern agree; asserted
				// rather than assumed, because the two patterns live in different
				// files and a change to one is not a change to the other.
				t.Errorf("%s matches a conformance test declaration but acID cannot read a "+
					"criterion out of it; testDefinition and acPattern have drifted apart", name)
				continue
			}
			covered[id] = append(covered[id], name)
		}
	}
	return covered
}

// notYetCovered names the requirements whose tranche has not been written.
//
// The list exists so that an incomplete repository can still hold itself to the
// 1:1 mapping for everything it has finished, rather than staying red until the
// last tranche lands and enforcing nothing in the meantime. It is deliberately
// spelled as an allowlist of *requirements* and checked in both directions: a
// requirement named here must have no tests at all, so landing GW-14's tranche
// without deleting its entry fails this criterion. The list can only shrink, and
// it reaches zero when the specifications and the suite agree.
var notYetCovered = map[string]bool{
	"GW-14": true,
}

// --- the criteria -----------------------------------------------------------

// TestGW10_AC1_CIRunsTheSuiteAgainstTheReferenceDeployment asserts the wiring
// for "the suite passes against the reference docker-compose deployment".
//
// The verdict itself is the workflow's exit code — a green run of this file is
// not evidence the deployment passed, and cannot be. What is checkable here is
// that the job still exists, still stands the reference deployment up, and still
// lets a failure fail: a `continue-on-error` or a trailing `|| true` would leave
// every one of these criteria reporting nothing at all while CI stayed green.
func TestGW10_AC1_CIRunsTheSuiteAgainstTheReferenceDeployment(t *testing.T) {
	beginOffline(t)

	job := workflowJob(t, "ci.yml", "conformance")

	for _, required := range []struct{ fragment, why string }{
		{"docker-compose.yml", "the job must run against the reference deployment, not a bespoke one"},
		{conformanceOverlay, "the deployment needs the mock provider the suite drives, which the overlay adds"},
		{"docker compose", "the deployment is brought up with compose"},
		{"--wait", "the suite must start against a deployment that is serving, not merely started"},
		{"./conformance/...", "the criterion names this package path"},
		{"-count=1", "Go caches test results, and a rerun whose only change is the target's " +
			"configuration would otherwise report the previous target's verdict"},
	} {
		if !strings.Contains(job, required.fragment) {
			t.Errorf("the conformance job does not mention %q: %s", required.fragment, required.why)
		}
	}

	for _, neutraliser := range []string{"continue-on-error", "|| true"} {
		if strings.Contains(job, neutraliser) {
			t.Errorf("the conformance job contains %q, which would let a failing suite report a "+
				"passing build; the criterion is that the run passes, so its exit code has to be the "+
				"job's", neutraliser)
		}
	}
}

// TestGW10_AC2_EveryAcceptanceCriterionHasExactlyOneTest is the coverage check
// GW-10 requires the repository to carry: "a script in the repo MUST verify
// every AC listed in spec/ has a matching test, and fail CI when one is missing."
//
// It is a test rather than a separate script so that `go test ./conformance/...`
// — the command CI already runs, and the one the criteria above name — is that
// script. A check that needs its own invocation is a check that can be left out
// of a workflow without anything noticing.
//
// Three directions, because a bijection is not one assertion:
//
//	spec → suite   every criterion outside the allowlist has a test;
//	allowlist      every allowlisted requirement has none, so the list shrinks;
//	suite → spec   every test names a criterion that exists, exactly once.
//
// The third is the one that catches a renamed criterion. A test for a GW-8.AC-9
// that the specification never defined would otherwise sit there passing.
func TestGW10_AC2_EveryAcceptanceCriterionHasExactlyOneTest(t *testing.T) {
	beginOffline(t)

	defined := specCriteria(t)
	covered := suiteTests(t)

	// spec → suite.
	var missing []string
	for id := range defined {
		if notYetCovered[requirementOf(id)] {
			continue
		}
		if len(covered[id]) == 0 {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d acceptance criteria have no test:\n  %s\n"+
			"Every criterion is covered by a test named TestGW<n>_AC<n>_<Description>, or its "+
			"requirement is named in notYetCovered.", len(missing), strings.Join(missing, "\n  "))
	}

	// The allowlist, in the direction that makes it shrink.
	for requirement := range notYetCovered {
		var written []string
		for id, tests := range covered {
			if requirementOf(id) == requirement {
				written = append(written, tests...)
			}
		}
		if len(written) > 0 {
			sort.Strings(written)
			t.Errorf("%s is listed in notYetCovered but %d of its tests exist (%s). "+
				"Remove the entry: the allowlist is what lets an unfinished repository enforce the "+
				"mapping for everything it has finished, and an entry that outlives its tranche "+
				"switches the enforcement back off for a requirement that no longer needs it",
				requirement, len(written), strings.Join(written, ", "))
		}
	}

	// suite → spec.
	for id, tests := range covered {
		if len(tests) > 1 {
			sort.Strings(tests)
			t.Errorf("%s is claimed by %d tests (%s); the mapping is 1:1, so two tests for one "+
				"criterion leave the report unable to say which of them is its verdict",
				id, len(tests), strings.Join(tests, ", "))
		}
		if defined[id] == "" {
			t.Errorf("%s is claimed by %s but no specification under spec/ defines it",
				id, strings.Join(tests, ", "))
		}
	}

	t.Logf("%d acceptance criteria defined, %d covered, %d awaiting a tranche (%s)",
		len(defined), len(covered), len(defined)-len(covered), strings.Join(sortedKeys(notYetCovered), ", "))
}

// TestGW10_AC3_TheSuiteIsIsolatedFromAConcurrentRun asserts the mechanism that
// makes two concurrent runs safe, and the wiring that exercises it.
//
// The criterion is about a second suite running at the same time, which no
// single run can observe. What holds it up is that every name this suite invents
// is derived from suite.runID — the tenant, the keys under it, the aliases, the
// mock models, the webhook sinks — so two runs share a gateway without ever
// addressing the same object.
//
// The mock is the exception, and the reason CI gives each concurrent run one of
// its own. Model-keyed fault state is isolated, but the listing fault
// (mockprovider.ListingTarget) and the per-model call counters on the seed
// models are process-global: GW-1.AC-6 breaks catalog listing for the whole
// mock, and GW-3.AC-4 asserts a delta on mock-chat-a's counter. Two runs sharing
// one mock would flake on both. The suite cannot fix that from inside, so it is
// a deployment requirement, asserted here as one.
func TestGW10_AC3_TheSuiteIsIsolatedFromAConcurrentRun(t *testing.T) {
	beginOffline(t)

	job := workflowJob(t, "ci.yml", "conformance")

	// Two runs, two mocks, two reports. A shared CONF_REPORT is the quiet
	// failure here: both runs pass and one of the two artefacts is overwritten
	// by the other, so the evidence for half the criterion disappears.
	for _, required := range []struct{ fragment, why string }{
		{mockServiceA, "the first concurrent run needs a mock of its own"},
		{mockServiceB, "the second concurrent run needs a mock of its own, because the listing " +
			"fault and the seed models' call counters are global to a mock process"},
		{"CONF_REPORT", "two runs writing conformance-report.json in one directory would leave " +
			"one run's report on disk and call it both"},
	} {
		if !strings.Contains(job, required.fragment) {
			t.Errorf("the conformance job does not mention %q: %s", required.fragment, required.why)
		}
	}

	if suite.cfg.BaseURL == "" {
		return
	}

	// The mechanism, against the live run: nothing this suite created is
	// addressable by a name a second run would also produce.
	if suite.runID == "" {
		t.Fatal("the suite has no run id, so every name it invents is one a concurrent run also invents")
	}
	if got := uniqueName("probe"); !strings.Contains(got, suite.runID) {
		t.Errorf("uniqueName(%q) = %q, which does not carry the run id %q", "probe", got, suite.runID)
	}

	tenants := suite.client.admin(t, http.MethodGet, "/admin/v1/tenants", nil)
	if tenants.Status != http.StatusOK {
		t.Fatalf("listing tenants: status %d\n%s", tenants.Status, truncate(tenants.Body))
	}
	name, found := tenantName(t, tenants.Body, suite.tenantID)
	if !found {
		t.Fatalf("the suite's own tenant %s is not in the tenant listing", suite.tenantID)
	}
	if !strings.Contains(name, suite.runID) {
		t.Errorf("the suite's tenant is named %q, which does not carry the run id %q; a second "+
			"concurrent run would create a tenant by the same name", name, suite.runID)
	}
}

// TestGW10_AC4_TheSuiteLeavesNothingBehind asserts that deleting a tenant
// removes what was created under it, verified the way the criterion names it:
// through the GW-6 list endpoints.
//
// TestMain already deletes the suite's own tenant and re-reads it to confirm it
// is gone, but that check is a GET by id and it runs after every test has
// finished — too late to be anyone's verdict. This performs the whole cycle in
// the open: a tenant, a key under it, a webhook subscribed to it, then a delete,
// then the listings.
//
// The webhook is the part worth spelling out. A gateway that dropped the tenant
// row and left its webhook subscriptions behind would keep signing deliveries
// for a tenant that no longer exists, which is a leak the tenant listing alone
// cannot see.
func TestGW10_AC4_TheSuiteLeavesNothingBehind(t *testing.T) {
	c := begin(t)

	tn := newTenant(t, "gw10-ac4")
	if suite.features["webhooks"] {
		newSink(t, tn.ID, "breaker.opened")
	}

	// Confirm the fixtures are real before asserting they are gone: a delete
	// that removes nothing passes every check below.
	keys := c.admin(t, http.MethodGet, "/admin/v1/tenants/"+tn.ID+"/keys", nil)
	if keys.Status != http.StatusOK {
		t.Fatalf("listing the new tenant's keys: status %d\n%s", keys.Status, truncate(keys.Body))
	}
	if n := len(listEnvelope(t, keys.Body)); n == 0 {
		t.Fatalf("the new tenant has no keys, so this criterion would pass without deleting anything")
	}

	deleted := c.admin(t, http.MethodDelete, "/admin/v1/tenants/"+tn.ID+"?confirm="+tn.ID, nil)
	if deleted.Status != http.StatusNoContent && deleted.Status != http.StatusOK {
		t.Fatalf("deleting tenant %s: status %d\n%s", tn.ID, deleted.Status, truncate(deleted.Body))
	}

	tenants := c.admin(t, http.MethodGet, "/admin/v1/tenants", nil)
	if tenants.Status != http.StatusOK {
		t.Fatalf("listing tenants: status %d\n%s", tenants.Status, truncate(tenants.Body))
	}
	if _, found := tenantName(t, tenants.Body, tn.ID); found {
		t.Errorf("tenant %s is still in the listing after being deleted", tn.ID)
	}

	// Its children with it. A 404 on the collection is the right answer once the
	// tenant is gone; an empty 200 is acceptable, and a populated one is not.
	for _, collection := range []string{"keys", "webhooks"} {
		resp := c.admin(t, http.MethodGet, "/admin/v1/tenants/"+tn.ID+"/"+collection, nil)
		switch resp.Status {
		case http.StatusNotFound:
		case http.StatusOK:
			if n := len(listEnvelope(t, resp.Body)); n > 0 {
				t.Errorf("%d %s survive tenant %s's deletion", n, collection, tn.ID)
			}
		default:
			t.Errorf("listing %s for the deleted tenant %s: status %d, want 404 or an empty 200\n%s",
				collection, tn.ID, resp.Status, truncate(resp.Body))
		}
	}
}

// TestGW10_AC5_AnUnclaimedCapabilitySkipsRatherThanFails asserts the selection
// rule that lets one suite run against deployments of different completeness:
// a requirement the target does not claim is skipped, and a skip does not change
// the exit code.
//
// The criterion is written about gw-12 because response caching is the clearest
// optional requirement, but nothing about it is specific to GW-12, and writing
// it that way would make the check evaporate the moment caching shipped. What is
// asserted is the rule over every requirement the target does not claim.
//
// The other half — that the exit code is still zero — belongs to the run rather
// than to any test, and checkCapabilityClaims is where it is enforced: an
// unclaimed requirement whose tests failed is reported there as a failure of the
// run. Here that leaves one thing to prove, which is that `gatedOut` is what
// decides, and that it says skip.
func TestGW10_AC5_AnUnclaimedCapabilitySkipsRatherThanFails(t *testing.T) {
	begin(t)

	var unclaimed []string
	for n := 1; n <= 14; n++ {
		requirement := fmt.Sprintf("GW-%d", n)
		if capabilityExempt[requirement] {
			// GW-10 and GW-11 are never claimed and never skipped; that is what
			// the exemption means, and asserting the general rule over them would
			// assert the opposite of it.
			continue
		}
		if suite.capabilities[capabilityID(requirement)] {
			continue
		}
		unclaimed = append(unclaimed, requirement)
		if !gatedOut(requirement) {
			t.Errorf("%s is absent from /v1/meta's capabilities but its section would still run; "+
				"an unclaimed requirement must skip, because claiming a capability and failing it is "+
				"a failure while not claiming it is not", requirement)
		}
	}

	// A target that claims everything proves nothing about the gate, and a run
	// against one would report this criterion as passing without exercising it.
	if len(unclaimed) == 0 {
		t.Skipf("the target claims every requirement, so there is no unclaimed section to gate; "+
			"this criterion needs a deployment with something switched off (version %s)", suite.version)
	}
	t.Logf("gated out: %s", strings.Join(unclaimed, ", "))
}

// TestGW10_AC6_TheContainerImageRunsFromTheDocumentedEnvironment asserts the
// wiring for the containerised run: the image exists, it runs the suite, and CI
// starts it with the three variables GW-10 documents and nothing else.
//
// The three-variable contract is the substance of this criterion. The suite
// reads six variables in all, and the other three — CONF_MOCK_CONTROL_URL,
// CONF_METRICS_TOKEN, CONF_LOG_PATH — exist for a host-side run where the mock
// and the gateway are reachable by different names than the suite's own. Inside
// the deployment's network none of that applies: the container dials the same
// service names the gateway does, which is why the contract can be three
// variables and why the image is the reference way to run the suite.
func TestGW10_AC6_TheContainerImageRunsFromTheDocumentedEnvironment(t *testing.T) {
	beginOffline(t)

	dockerfile := repoFile(t, "conformance", "Dockerfile")
	for _, required := range []struct{ fragment, why string }{
		{"go test -c", "the image ships a compiled test binary, so the run needs no toolchain"},
		{"spec", "the criteria in gw10_test.go read spec/, so it has to be in the image"},
		{"conformance-report.json", "the criterion is that the run produces the report"},
	} {
		if !strings.Contains(dockerfile, required.fragment) {
			t.Errorf("conformance/Dockerfile does not mention %q: %s", required.fragment, required.why)
		}
	}

	job := workflowJob(t, "ci.yml", "conformance")
	if !strings.Contains(job, "docker run") {
		t.Error("the conformance job never runs the image, so the containerised path is published " +
			"untested; the criterion is about the image, not only about the suite")
	}

	// The environment the image is started with, exactly. An extra variable here
	// is not a small thing: it means the documented contract is not the contract,
	// and that an operator following the specification gets a run the suite's own
	// CI never performs.
	documented := map[string]bool{
		"CONF_BASE_URL":      true,
		"CONF_ADMIN_KEY":     true,
		"CONF_MOCK_PROVIDER": true,
	}
	for _, passed := range containerEnvNames(t, job) {
		if !documented[passed] {
			t.Errorf("the containerised run is given %s, which is not one of the three variables "+
				"GW-10 documents; the image must run from CONF_BASE_URL, CONF_ADMIN_KEY and "+
				"CONF_MOCK_PROVIDER alone", passed)
		}
		delete(documented, passed)
	}
	for name := range documented {
		t.Errorf("the containerised run is not given %s, which the suite needs to reach its target", name)
	}
}

// --- reading the workflow ---------------------------------------------------

// conformanceOverlay, mockServiceA and mockServiceB are named here rather than
// spelled inline so the two criteria that assert them, the compose overlay and
// the workflow cannot drift apart silently.
const (
	conformanceOverlay = "docker-compose.conformance.yml"
	mockServiceA       = "mock-provider-a"
	mockServiceB       = "mock-provider-b"
)

// workflowJobStart matches a job key: two spaces of indent, at the top level of
// the `jobs:` map.
var workflowJobStart = regexp.MustCompile(`(?m)^  [A-Za-z][\w-]*:$`)

// workflowJob returns the body of one job from a workflow file.
//
// This reads the YAML as text rather than parsing it. The suite is standard
// library only — deliberately, so that what it depends on cannot drift from what
// it asserts — and the assertions here are all "is this still wired up", which
// a substring answers as well as a parse would. What a parse would add is
// precision about *where* a fragment appears, and the cost of not having it is
// that a fragment in a comment counts; that is a tolerable trade for a tripwire.
func workflowJob(t *testing.T, workflow, job string) string {
	t.Helper()

	body := repoFile(t, ".github", "workflows", workflow)
	header := "\n  " + job + ":\n"
	start := strings.Index(body, header)
	if start < 0 {
		t.Fatalf("%s has no %q job. GW-10 requires this repository's CI to run the full suite "+
			"against a fresh docker-compose deployment on every merge to main, and the job is where "+
			"that happens", workflow, job)
	}

	rest := body[start+len(header):]
	if next := workflowJobStart.FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	return rest
}

// containerEnvNames returns the variable names passed to `docker run` with -e in
// the job, which is the environment the image is started with.
var dockerRunEnv = regexp.MustCompile(`-e\s+([A-Z][A-Z0-9_]*)`)

func containerEnvNames(t *testing.T, job string) []string {
	t.Helper()

	start := strings.Index(job, "docker run")
	if start < 0 {
		return nil
	}
	// To the end of the shell command: a blank line, or the next step.
	command := job[start:]
	if end := strings.Index(command, "\n\n"); end > 0 {
		command = command[:end]
	}

	var names []string
	for _, match := range dockerRunEnv.FindAllStringSubmatch(command, -1) {
		names = append(names, match[1])
	}
	sort.Strings(names)
	return names
}

// --- small readers ----------------------------------------------------------

// listEnvelope returns the items of a GW-6 list response.
func listEnvelope(t *testing.T, body []byte) []map[string]any {
	t.Helper()

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("parsing a list response: %v\n%s", err, truncate(body))
	}
	return envelope.Data
}

// tenantName finds a tenant in a listing by id and returns its name.
func tenantName(t *testing.T, body []byte, id string) (string, bool) {
	t.Helper()

	for _, item := range listEnvelope(t, body) {
		if item["id"] == id {
			name, _ := item["name"].(string)
			return name, true
		}
	}
	return "", false
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
