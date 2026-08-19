package conformance

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-11 — the deployment criteria.
//
// Every other section in this suite asks a running gateway questions over HTTP.
// These ask questions about the *deployment*: how it starts, what it does when a
// dependency is missing, and how it stops. Four of the six cannot be answered by
// talking to a gateway someone else started, because the thing under test is the
// starting and the stopping — so those four build the binary and run it here,
// with an environment this file controls.
//
// That is the same reason GW-11 sits in capabilityExempt alongside GW-10: a
// running process has nothing to claim about its own deployment, so /v1/meta
// will never list `gw-11` and these criteria run whenever they can rather than
// when they are claimed. Nothing here reads suite.capabilities.
//
// The four spawning criteria skip — rather than fail — where the gateway source
// or a Go toolchain is absent. That is the containerised run from GW-10.AC-6,
// which carries a compiled test binary and spec/ and nothing else. A skip there
// is honest: the criteria were not checked, and the host-side run in the same CI
// job is where they are.

// --- building and running the gateway ----------------------------------------

// buildGateway compiles the gateway into the calling test's temporary directory.
//
// Built per test rather than once for the file: `go build` after the first is a
// relink against a warm build cache, which costs a second or two, and the price
// buys automatic cleanup instead of a package-level temporary directory that
// has to outlive every test and be removed by something that is not a test.
//
// The gateway is a separate module — gateway/go.mod, module
// github.com/cognigate/gateway — so this runs with that directory as its
// working directory rather than reaching for a package path from here.
func buildGateway(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(sourceRoot(t), "gateway")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Skipf("no gateway source at %s: this criterion runs the gateway binary, which the "+
			"containerised suite (GW-10.AC-6) does not carry", dir)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH: this criterion builds and runs the gateway binary")
	}

	bin := filepath.Join(t.TempDir(), "cognigate")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		// A build failure is a real regression, not a reason to skip: the source
		// is present and it does not compile.
		t.Fatalf("building the gateway in %s: %v\n%s", dir, err, out)
	}
	return bin
}

// syncBuffer collects a child process's output from the goroutines os/exec
// writes it on, so a test can read it while the process is still running.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gateway is a gateway process this test started.
type gateway struct {
	// URL is the base the client dials, scheme included: https when the process
	// was given a keypair.
	URL string
	// Port is the port it was told to listen on, which a test needs when it has
	// to reach the same port by another scheme.
	Port int
	// dataKey is the credential the data plane reads with, set by the test once
	// the dev banner has been parsed.
	dataKey string

	c      *client
	cmd    *exec.Cmd
	out    *syncBuffer
	exited chan error
}

// spawn starts a gateway on its own port with an environment this test controls.
//
// The environment is filtered rather than extended. config.applyEnv reads
// CG_<NAME> before <NAME>, so an ambient CG_ANALYTICS_URL on the runner would
// outrank an ANALYTICS_URL set here and the test would quietly measure the
// wrong process. Both spellings of every name this call sets are removed first,
// along with the two that would change what `--dev` means.
//
// A non-nil roots pool means the process was given a keypair: the client dials
// https and verifies against that pool. Verification is kept on rather than
// skipped — the pool holds the one certificate this test generated, so it costs
// nothing to check, and a suite that habitually disabled verification would be a
// poor advertisement for a product whose own criterion is that TLS is configured
// properly.
func spawn(t *testing.T, args []string, env map[string]string, roots *x509.CertPool) *gateway {
	t.Helper()

	bin := buildGateway(t)
	port := freePort(t)

	overrides := map[string]string{"PORT": strconv.Itoa(port)}
	for k, v := range env {
		overrides[k] = v
	}
	// Never inherited: a bootstrap key from the runner's environment would
	// replace the fixed dev credential these criteria name, and a config path
	// would replace the defaults they assume.
	blocked := map[string]bool{"ADMIN_BOOTSTRAP_KEY": true, "CONFIG": true}

	cmd := exec.Command(bin, args...)
	cmd.Env = childEnv(overrides, blocked)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the gateway: %v", err)
	}

	scheme := "http"
	transport := http.DefaultTransport
	if roots != nil {
		scheme = "https"
		transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	}

	g := &gateway{
		URL:    fmt.Sprintf("%s://127.0.0.1:%d", scheme, port),
		Port:   port,
		cmd:    cmd,
		out:    out,
		exited: make(chan error, 1),
	}
	g.c = &client{base: g.URL, http: &http.Client{Timeout: 30 * time.Second, Transport: transport}}

	go func() { g.exited <- cmd.Wait() }()

	t.Cleanup(func() {
		// Kill rather than signal: the criteria that care how it stops do their
		// own signalling and wait for the exit themselves, and everything else
		// wants the port back without a drain in the way.
		_ = cmd.Process.Kill()
		<-g.exited
	})
	return g
}

// childEnv copies the current environment minus anything that would outrank an
// override, then appends the overrides. An override with an empty value is a
// removal.
func childEnv(overrides map[string]string, blocked map[string]bool) []string {
	drop := map[string]bool{}
	for name := range overrides {
		drop[name] = true
		drop["CG_"+name] = true
	}
	for name := range blocked {
		drop[name] = true
		drop["CG_"+name] = true
	}

	var env []string
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && drop[name] {
			continue
		}
		env = append(env, entry)
	}
	for name, value := range overrides {
		if value == "" {
			continue
		}
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}

// freePort returns a port nothing is listening on.
//
// Racy in principle and settled in practice: the kernel does not hand the same
// ephemeral port to two callers in the window between the close here and the
// bind in the child. GW-11.AC-3 needs the port back after the fact, which is
// why this returns a number rather than a listener to hand over.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return port
}

// awaitServing polls /healthz until the gateway answers, and fails with the
// child's own output when it does not. Startup failures — a bad keypair, a port
// in use — are written there and are the whole explanation.
func (g *gateway) awaitServing(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-g.exited:
			g.exited <- err
			t.Fatalf("the gateway exited during startup (%v):\n%s", err, g.out.String())
		default:
		}

		resp, err := g.c.try(http.MethodGet, "/healthz", "", nil)
		if err == nil && resp.Status == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway never answered GET %s/healthz (last: %v):\n%s",
				g.URL, err, g.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// awaitOutput waits for a line matching needle in the child's output.
func (g *gateway) awaitOutput(t *testing.T, needle string, patience time.Duration) {
	t.Helper()

	deadline := time.Now().Add(patience)
	for {
		if strings.Contains(g.out.String(), needle) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway never logged anything containing %q within %s:\n%s",
				needle, patience, g.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// --- reading the dev banner ---------------------------------------------------

// The banner is a contract with a human, so it is matched by the labels a human
// reads rather than by position. The two key patterns are anchored on their
// prefixes because GW-11.AC-2 names those prefixes: a data key that stopped
// being `cg-dev-` would be a different criterion's failure, not a parse error.
var (
	devTenantLine   = regexp.MustCompile(`Tenant\s+(\S+)`)
	devDataKeyLine  = regexp.MustCompile(`Data key\s+(cg-dev-\S+)`)
	devAdminKeyLine = regexp.MustCompile(`Admin key\s+(cga-dev-\S+)`)
)

type devIdentity struct {
	TenantID string
	DataKey  string
	AdminKey string
}

// awaitDevBanner reads the credentials a `--dev` process prints.
//
// Polled rather than read once. The banner is printed before the listener opens
// and os/exec delivers it on another goroutine, so "the process is serving" and
// "its output has reached this buffer" are two events with no ordering between
// them.
func (g *gateway) awaitDevBanner(t *testing.T) devIdentity {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		out := g.out.String()
		tenant := devTenantLine.FindStringSubmatch(out)
		data := devDataKeyLine.FindStringSubmatch(out)
		admin := devAdminKeyLine.FindStringSubmatch(out)
		if tenant != nil && data != nil && admin != nil {
			return devIdentity{TenantID: tenant[1], DataKey: data[1], AdminKey: admin[1]}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the dev banner did not print a tenant, a cg-dev- key and a cga-dev- key:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// localMock serves the suite's mock upstream on a loopback address the spawned
// gateway can dial.
//
// Not startMock: that one returns the mock the *target* deployment reaches,
// which in the reference deployment is a compose service name that resolves
// only inside that network. A process started here has to be given something
// this host can route to.
func localMock(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(mockprovider.New().Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// registerMock points a spawned gateway's tenant at an upstream and waits for
// the catalog to carry the named model.
func (g *gateway) registerMock(t *testing.T, adminKey, tenantID, mockURL, model string) {
	t.Helper()

	resp, err := g.c.try(http.MethodPost, "/admin/v1/tenants/"+tenantID+"/providers", adminKey,
		map[string]any{
			"name":     "mock",
			"kind":     "openai",
			"base_url": mockURL,
			"keys":     []string{"mock-key-primary"},
		})
	if err != nil {
		t.Fatalf("registering the mock: %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Fatalf("registering the mock: status %d\n%s", resp.Status, truncate(resp.Body))
	}

	// The catalog is refreshed rather than written through, so the provider
	// existing and its models being listed are two events. Every test here goes
	// on to send a completion, which would fail on a model the router has not
	// seen yet.
	deadline := time.Now().Add(20 * time.Second)
	for {
		listed, err := g.c.try(http.MethodGet, "/v1/models", g.dataKey, nil)
		if err == nil && listed.Status == http.StatusOK &&
			bytes.Contains(listed.Body, []byte(`"`+model+`"`)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the catalog never listed %q after the provider was registered", model)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// slowUpstream serves an OpenAI-shaped provider whose streaming completion
// stops halfway and waits.
//
// GW-11.AC-5 is about a signal arriving *during* a stream, and the suite's own
// mock answers as fast as the loopback allows: against it, the stream would
// usually be closed before the signal was delivered and the criterion's central
// assertion would pass without ever having been tested. Pausing between the
// first frame and the rest makes the window real, so a gateway that dropped
// in-flight streams on SIGTERM fails here every time rather than occasionally.
func slowUpstream(t *testing.T, model string, pause time.Duration) string {
	t.Helper()

	chunk := func(text string) string {
		return fmt.Sprintf(
			`data: {"id":"chatcmpl-gw11","object":"chat.completion.chunk","model":%q,`+
				`"choices":[{"index":0,"delta":{"content":%q}}]}`+"\n\n", model, text)
	}

	listModels := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": model, "object": "model", "owned_by": "conformance"}},
		})
	}
	chatCompletions := func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		_, _ = io.WriteString(w, chunk("hi"))
		flusher.Flush()

		// The pause is what the signal is aimed at. Abandoned if the client
		// disappears, so a failing test does not hold the server open.
		select {
		case <-time.After(pause):
		case <-r.Context().Done():
			return
		}

		_, _ = io.WriteString(w, chunk(" there"))
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	// Registered bare and under /v1, for the reason mockprovider.Handler gives:
	// the adapter joins "/models" onto whatever base URL was registered, so an
	// upstream that answers only one spelling is a 404 to half of them.
	mux := http.NewServeMux()
	for _, prefix := range []string{"", "/v1"} {
		mux.HandleFunc("GET "+prefix+"/models", listModels)
		mux.HandleFunc("POST "+prefix+"/chat/completions", chatCompletions)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// chat sends a minimal completion to a spawned gateway. The prompt is trivial
// for the same reason the harness's is: GW-14 forbids retaining content, and a
// distinctive one invites someone to satisfy a test by retaining it.
func (g *gateway) chat(t *testing.T, key, model string) *response {
	t.Helper()

	resp, err := g.c.try(http.MethodPost, "/v1/chat/completions", key, map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("POST %s/v1/chat/completions: %v", g.URL, err)
	}
	return resp
}

// --- GW-11.AC-1 ---------------------------------------------------------------

// TestGW11_AC1_TheReferenceDeploymentComesUpHealthy asserts what makes
// `docker compose up -d --wait` a truthful signal.
//
// The criterion is a stopwatch on a clean checkout, which no test inside the
// deployment can hold. What decides whether the stopwatch measures anything is
// health-check coverage: `--wait` returns when every service that *has* a health
// check is healthy, so a service without one is waited on for nothing and the
// command returns while it is still starting. A stack where that is true would
// pass a 60-second budget by not measuring one of its services.
//
// So this checks the two things that make the budget meaningful — every service
// is health-checked somewhere, and CI actually starts the stack with `--wait` —
// and leaves "the suite passes against it" to GW-10.AC-1, which is the criterion
// that owns the conformance job.
func TestGW11_AC1_TheReferenceDeploymentComesUpHealthy(t *testing.T) {
	beginOffline(t)

	compose := repoFile(t, composeFile)

	// The two images this repository builds carry their own HEALTHCHECK, which
	// compose honours; the stock images do not ship one, so the compose file has
	// to supply it. Either is fine — having neither is not.
	inDockerfile := map[string]string{
		"gateway":   "gateway",
		"analytics": "analytics",
	}
	for _, service := range composeServices(t, compose) {
		if composeServiceHasHealthcheck(compose, service) {
			continue
		}
		dir, built := inDockerfile[service]
		if !built {
			t.Errorf("the %s service in %s has no healthcheck, so `docker compose up --wait` "+
				"returns without waiting for it; a stack that reports all-healthy while one "+
				"service is still starting cannot be held to a start-up budget", service, composeFile)
			continue
		}
		if !strings.Contains(repoFile(t, dir, "Dockerfile"), "HEALTHCHECK") {
			t.Errorf("the %s service has no healthcheck in %s and its Dockerfile declares none "+
				"either, so `--wait` does not wait for it", service, composeFile)
		}
	}

	job := workflowJob(t, "ci.yml", smokeJob)
	for _, required := range []struct{ fragment, why string }{
		{"docker compose up", "the criterion is about bringing the reference deployment up"},
		{"--wait", "without it the job continues against a stack that has started but is not serving, " +
			"which is the state this criterion exists to rule out"},
	} {
		if !strings.Contains(job, required.fragment) {
			t.Errorf("the %s job does not mention %q: %s", smokeJob, required.fragment, required.why)
		}
	}
	for _, neutraliser := range []string{"continue-on-error", "|| true"} {
		if strings.Contains(job, neutraliser) {
			t.Errorf("the %s job contains %q, which would let a stack that never came up report a "+
				"passing build", smokeJob, neutraliser)
		}
	}

	// Against a live target, the one half of the criterion a running deployment
	// can answer: it is serving.
	if suite.cfg.BaseURL == "" {
		return
	}
	resp, err := suite.client.try(http.MethodGet, "/healthz", "", nil)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("GET /healthz: status %d, want 200 from a deployment that is up\n%s",
			resp.Status, truncate(resp.Body))
	}
}

// composeFile and smokeJob are named rather than spelled inline so this file,
// the workflow and gw10_test.go's own reference to the compose file cannot drift
// apart silently.
const (
	composeFile = "docker-compose.yml"
	smokeJob    = "integration-smoke"
)

// composeServiceKey matches a service name: four spaces of indent, directly
// under `services:`.
var composeServiceKey = regexp.MustCompile(`(?m)^  ([a-z][\w-]*):$`)

// composeServices returns the service names in a compose file.
//
// Read as text, like workflowJob and for the same reason: the suite is standard
// library only, so that what it depends on cannot drift from what it asserts.
// The `services:` block is sliced out first, because the file's `networks:` and
// `volumes:` maps are indented identically.
func composeServices(t *testing.T, compose string) []string {
	t.Helper()

	start := strings.Index(compose, "\nservices:\n")
	if start < 0 {
		t.Fatalf("%s has no services: block", composeFile)
	}
	body := compose[start+len("\nservices:\n"):]
	if next := regexp.MustCompile(`(?m)^[a-z]`).FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}

	var names []string
	for _, m := range composeServiceKey.FindAllStringSubmatch(body, -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatalf("no services found in %s", composeFile)
	}
	sort.Strings(names)
	return names
}

// composeServiceHasHealthcheck reports whether one service declares a
// healthcheck, by slicing its block out and looking inside it.
func composeServiceHasHealthcheck(compose, service string) bool {
	header := "\n  " + service + ":\n"
	start := strings.Index(compose, header)
	if start < 0 {
		return false
	}
	block := compose[start+len(header):]
	if next := composeServiceKey.FindStringIndex(block); next != nil {
		block = block[:next[0]]
	}
	return strings.Contains(block, "healthcheck:")
}

// --- GW-11.AC-2 ---------------------------------------------------------------

// TestGW11_AC2_DevModeRunsTheWholeProductInOneProcess starts `cognigate --dev`
// with nothing else on the machine and drives both planes through it.
//
// The point of the criterion is that a developer needs no Redis, no Postgres and
// no analytics service to exercise the product, so the test gives the process
// none of those: the environment it inherits is stripped of every variable that
// would attach one. What is left has to serve a completion and the admin CRUD
// on its own.
//
// "The full /admin/v1 CRUD from GW-6" is read here as the four verbs against one
// resource, not as a second copy of GW-6's section. Aliases are that resource
// because they are the one where create and update are the same call, so a
// gateway that accepted the write and never stored it fails the read.
func TestGW11_AC2_DevModeRunsTheWholeProductInOneProcess(t *testing.T) {
	beginOffline(t)

	// No REDIS_URL, no DATABASE_URL, no ANALYTICS_URL. If any of these ever
	// becomes load-bearing, this process will not start and the criterion will
	// say so.
	g := spawn(t, []string{"--dev"}, map[string]string{
		"ANALYTICS_URL":   "",
		"ANALYTICS_TOKEN": "",
		"REDIS_URL":       "",
		"DATABASE_URL":    "",
	}, nil)
	dev := g.awaitDevBanner(t)
	g.awaitServing(t)

	// The prefixes are the criterion, not decoration: they are what tells a
	// developer at a glance that a key came from a throwaway process, and the
	// banner is where the product promises them.
	if !strings.HasPrefix(dev.DataKey, "cg-dev-") {
		t.Errorf("the printed data key is %q, which is not a cg-dev- key", dev.DataKey)
	}
	if !strings.HasPrefix(dev.AdminKey, "cga-dev-") {
		t.Errorf("the printed admin key is %q, which is not a cga-dev- key", dev.AdminKey)
	}

	g.dataKey = dev.DataKey
	g.registerMock(t, dev.AdminKey, dev.TenantID, localMock(t), "mock-chat-a")

	// The data plane, end to end through a real upstream.
	resp := g.chat(t, dev.DataKey, "mock-chat-a")
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions on a dev process: status %d\n%s",
			resp.Status, truncate(resp.Body))
	}
	var completion struct {
		Object string `json:"object"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(resp.Body, &completion); err != nil {
		t.Fatalf("parsing the completion: %v\n%s", err, truncate(resp.Body))
	}
	if completion.Object != "chat.completion" {
		t.Errorf("the completion's object is %q, want %q", completion.Object, "chat.completion")
	}

	// The admin plane: create, read, update, delete, read again.
	base := "/admin/v1/tenants/" + dev.TenantID
	name := "gw11-ac2-alias"

	created, err := g.c.try(http.MethodPut, base+"/aliases/"+name, dev.AdminKey,
		map[string]any{"capabilities": []string{"chat"}})
	if err != nil {
		t.Fatalf("PUT an alias: %v", err)
	}
	if created.Status != http.StatusOK {
		t.Fatalf("PUT an alias on a dev process: status %d\n%s", created.Status, truncate(created.Body))
	}

	if !g.aliasListed(t, dev.AdminKey, dev.TenantID, name) {
		t.Fatalf("the alias %q is not in the listing after being created", name)
	}

	// The update is the same verb, which is what makes the read below the only
	// evidence it happened.
	updated, err := g.c.try(http.MethodPut, base+"/aliases/"+name, dev.AdminKey,
		map[string]any{"capabilities": []string{"chat"}, "pin": "mock-chat-a"})
	if err != nil {
		t.Fatalf("updating the alias: %v", err)
	}
	if updated.Status != http.StatusOK {
		t.Fatalf("updating the alias: status %d\n%s", updated.Status, truncate(updated.Body))
	}
	if served := g.chat(t, dev.DataKey, name); served.Status != http.StatusOK {
		t.Errorf("the updated alias does not serve: status %d\n%s", served.Status, truncate(served.Body))
	}

	deleted, err := g.c.try(http.MethodDelete, base+"/aliases/"+name, dev.AdminKey, nil)
	if err != nil {
		t.Fatalf("deleting the alias: %v", err)
	}
	if deleted.Status != http.StatusNoContent && deleted.Status != http.StatusOK {
		t.Fatalf("deleting the alias: status %d\n%s", deleted.Status, truncate(deleted.Body))
	}
	if g.aliasListed(t, dev.AdminKey, dev.TenantID, name) {
		t.Errorf("the alias %q is still in the listing after being deleted", name)
	}

	// Keys, because they are the resource a dev process mints for itself and the
	// one GW-6 makes the admin plane's reason to exist.
	minted, err := g.c.try(http.MethodPost, base+"/keys", dev.AdminKey,
		map[string]any{"name": "gw11-ac2", "plane": "data"})
	if err != nil {
		t.Fatalf("minting a key: %v", err)
	}
	if minted.Status != http.StatusCreated {
		t.Fatalf("minting a key on a dev process: status %d\n%s", minted.Status, truncate(minted.Body))
	}
	var mintedKey struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(minted.Body, &mintedKey); err != nil || mintedKey.Secret == "" {
		t.Fatalf("the minted key has no secret: %s", truncate(minted.Body))
	}
	if mintedKey.Key.ID == "" {
		t.Fatalf("the minted key has no id, so it can never be revoked: %s", truncate(minted.Body))
	}
	if got := g.chat(t, mintedKey.Secret, "mock-chat-a"); got.Status != http.StatusOK {
		t.Errorf("a key minted through the dev admin plane does not open the data plane: status %d\n%s",
			got.Status, truncate(got.Body))
	}
	revoked, err := g.c.try(http.MethodDelete, base+"/keys/"+mintedKey.Key.ID, dev.AdminKey, nil)
	if err != nil {
		t.Fatalf("revoking the key: %v", err)
	}
	if revoked.Status != http.StatusNoContent && revoked.Status != http.StatusOK {
		t.Fatalf("revoking a key: status %d\n%s", revoked.Status, truncate(revoked.Body))
	}
	// The status code is not the criterion; the key no longer working is. A
	// revocation that returned 204 and left the credential live would be the
	// worst possible outcome to report as a pass.
	if got := g.chat(t, mintedKey.Secret, "mock-chat-a"); got.Status != http.StatusUnauthorized {
		t.Errorf("a revoked key still opens the data plane: status %d, want 401", got.Status)
	}
}

// aliasListed reports whether an alias is in the tenant's listing.
func (g *gateway) aliasListed(t *testing.T, adminKey, tenantID, name string) bool {
	t.Helper()

	resp, err := g.c.try(http.MethodGet, "/admin/v1/tenants/"+tenantID+"/aliases", adminKey, nil)
	if err != nil {
		t.Fatalf("listing aliases: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("listing aliases: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	for _, item := range listEnvelope(t, resp.Body) {
		if item["name"] == name || item["id"] == name {
			return true
		}
	}
	return false
}

// --- GW-11.AC-3 ---------------------------------------------------------------

// TestGW11_AC3_TheDataPlaneOutlivesAnAnalyticsOutage takes the metering service
// away and asserts that the product keeps working without it.
//
// The outage is arranged rather than simulated: a port is reserved, released,
// and the gateway is pointed at it, so every delivery attempt fails to connect
// exactly as it would against a stopped container. Recovery is the same port
// coming back — which is why freePort returns a number instead of a listener.
//
// Three things follow from the criterion, and all three are asserted here. A
// completion succeeds while metering cannot be delivered, because the gateway
// fails open on accounting rather than refusing traffic it cannot bill. A
// warning is logged, because silence would make an outage that costs money
// invisible. And the usage that could not be delivered arrives once the service
// answers, because a buffer that dropped what it held would make the first two
// a way of losing revenue quietly.
func TestGW11_AC3_TheDataPlaneOutlivesAnAnalyticsOutage(t *testing.T) {
	beginOffline(t)

	analyticsPort := freePort(t)
	analyticsURL := fmt.Sprintf("http://127.0.0.1:%d", analyticsPort)

	g := spawn(t, []string{"--dev"}, map[string]string{
		"ANALYTICS_URL":   analyticsURL,
		"ANALYTICS_TOKEN": "gw11-ac3-token-not-a-real-credential",
	}, nil)
	dev := g.awaitDevBanner(t)
	g.awaitServing(t)

	g.dataKey = dev.DataKey
	g.registerMock(t, dev.AdminKey, dev.TenantID, localMock(t), "mock-chat-a")

	// The data plane, with nothing listening on the analytics port.
	if resp := g.chat(t, dev.DataKey, "mock-chat-a"); resp.Status != http.StatusOK {
		t.Fatalf("a completion failed while analytics was down: status %d\n%s\n%s",
			resp.Status, truncate(resp.Body), g.out.String())
	}

	// The warning. Matched on the part of the sentence that is the signal rather
	// than on the whole line, which carries a timestamp and a cause.
	g.awaitOutput(t, "usage records are not reaching analytics", 30*time.Second)

	// The reads cannot be answered while the service they come from is down, and
	// that is a dependency being unavailable rather than the gateway being
	// broken — the distinction a client uses to decide between retrying and
	// escalating.
	if resp, err := g.c.try(http.MethodGet, "/v1/usage?window=day", dev.DataKey, nil); err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	} else if resp.Status != http.StatusServiceUnavailable {
		t.Errorf("GET /v1/usage while analytics is down: status %d, want 503; a usage read that "+
			"cannot reach its source is unavailable, not a server error\n%s",
			resp.Status, truncate(resp.Body))
	}

	// Analytics comes back on the port it was always supposed to be on.
	recorded := startFakeAnalytics(t, analyticsPort)

	// The buffered record arrives. Delivery is retried with a doubling backoff
	// from 500ms, so a short outage is redelivered in seconds; the patience here
	// is for a loaded runner, not for the backoff.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if recorded() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no usage record reached analytics after it came back; the buffer dropped what "+
				"it was holding\n%s", g.out.String())
		}
		time.Sleep(250 * time.Millisecond)
	}

	// And it is what GET /v1/usage now reports, which is where the criterion
	// says the recovered usage has to appear.
	resp, err := g.c.try(http.MethodGet, "/v1/usage?window=day", dev.DataKey, nil)
	if err != nil {
		t.Fatalf("GET /v1/usage after recovery: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/usage after recovery: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	var report usageReport
	if err := json.Unmarshal(resp.Body, &report); err != nil {
		t.Fatalf("parsing GET /v1/usage: %v\n%s", err, truncate(resp.Body))
	}
	if report.Requests < 1 {
		t.Errorf("GET /v1/usage reports %d requests after the buffered record was delivered, want at "+
			"least 1; the usage plane is answering from somewhere the delivery did not reach",
			report.Requests)
	}
}

// startFakeAnalytics serves the three endpoints the gateway's analytics client
// calls, on one specific port, and reports how many usage records it has been
// given.
//
// A specific port rather than httptest's own, because the gateway was started
// against this address before anything was listening on it: the outage and the
// recovery have to be the same endpoint or the test proves nothing about
// reconnection.
func startFakeAnalytics(t *testing.T, port int) func() int {
	t.Helper()

	var mu sync.Mutex
	var count int

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/v1/usage/totals", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n := count
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"requests": n, "prompt_tokens": n, "completion_tokens": n,
			"total_tokens": 2 * n, "cost_usd": 0,
		})
	})
	mux.HandleFunc("/api/v1/usage/breakdown", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("restarting analytics on port %d: %v", port, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// --- GW-11.AC-4 ---------------------------------------------------------------

// TestGW11_AC4_ConfiguredTLSServesHTTPSAndRefusesPlaintext gives the gateway a
// keypair and checks both halves of what that has to mean.
//
// Serving HTTPS is the easy half. The half worth a test is that the same port
// stops speaking plaintext: a deployment where `https://` works and `http://`
// also quietly works has a downgrade nobody configured, and a client that got
// the scheme wrong would send its credential in the clear and see a 200 for it.
func TestGW11_AC4_ConfiguredTLSServesHTTPSAndRefusesPlaintext(t *testing.T) {
	beginOffline(t)

	certFile, keyFile, roots := selfSignedKeypair(t)
	g := spawn(t, []string{"--dev"}, map[string]string{
		"TLS_CERT_FILE":   certFile,
		"TLS_KEY_FILE":    keyFile,
		"ANALYTICS_URL":   "",
		"ANALYTICS_TOKEN": "",
	}, roots)
	g.awaitServing(t)

	// HTTPS, with a credential, all the way to a handler that needs one.
	dev := g.awaitDevBanner(t)
	health, err := g.c.try(http.MethodGet, "/v1/health", dev.DataKey, nil)
	if err != nil {
		t.Fatalf("GET %s/v1/health over TLS: %v", g.URL, err)
	}
	if health.Status != http.StatusOK {
		t.Errorf("GET /v1/health over TLS: status %d\n%s", health.Status, truncate(health.Body))
	}

	// Plaintext to the same port. A TLS listener answers a plaintext request
	// with a TLS alert rather than an HTTP response, so the transport error is
	// the conformant outcome and any status code at all is the failure.
	plain := &http.Client{Timeout: 10 * time.Second}
	resp, err := plain.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", g.Port))
	if err == nil {
		defer resp.Body.Close()
		t.Errorf("GET http://127.0.0.1:%d/healthz answered %d; with a keypair configured the port "+
			"must not serve plaintext, or a client that got the scheme wrong sends its key in the "+
			"clear and never finds out", g.Port, resp.StatusCode)
	}
}

// selfSignedKeypair writes a certificate and key for 127.0.0.1 to the test's
// temporary directory and returns the two paths, plus a pool that trusts the
// certificate so the client can verify rather than skip.
//
// Generated rather than committed. A keypair in the repository is a private key
// in the repository — it would be found by every secret scanner pointed at this
// project, and correctly so — and one with a fixed expiry turns into a test that
// fails on a date nobody chose.
func selfSignedKeypair(t *testing.T) (certFile, keyFile string, roots *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating a serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cognigate-conformance"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("encoding the key: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")
	write := func(path, blockType string, der []byte) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	write(certFile, "CERTIFICATE", der)
	write(keyFile, "EC PRIVATE KEY", keyDER)

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the certificate back: %v", err)
	}
	roots = x509.NewCertPool()
	roots.AddCert(cert)
	return certFile, keyFile, roots
}

// --- GW-11.AC-5 ---------------------------------------------------------------

// TestGW11_AC5_SIGTERMDrainsWithoutCuttingAStream signals a gateway in the
// middle of a streaming completion and watches how it stops.
//
// The four things the criterion asks for are four different promises, and the
// order they are checked in is the order they matter. The stream finishes,
// because cutting a caller off mid-completion loses work they have already been
// charged for. A request arriving afterwards is refused rather than served,
// because a process on its way out should not take on new obligations. /healthz
// fails, because that is the signal a load balancer steers on and it has to
// change before the refusals start rather than after. And the process exits 0,
// because an orchestrator reads a non-zero status as a crash and escalates a
// clean shutdown into an incident.
//
// The request after SIGTERM goes down the connection the stream used. Fiber
// stops accepting new connections the moment shutdown begins, so a fresh dial is
// refused by the kernel and never reaches the middleware that answers 503 —
// which is a conformant refusal too, and the assertion below accepts either,
// but only the pooled connection can demonstrate the documented one.
func TestGW11_AC5_SIGTERMDrainsWithoutCuttingAStream(t *testing.T) {
	beginOffline(t)

	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM cannot be delivered to a process on Windows; this criterion is checked " +
			"on the Linux runners, which is where the reference deployment runs")
	}

	g := spawn(t, []string{"--dev"}, map[string]string{
		"ANALYTICS_URL":   "",
		"ANALYTICS_TOKEN": "",
	}, nil)
	dev := g.awaitDevBanner(t)
	g.awaitServing(t)

	// The pause has to outlast the round trip from signalling to reading the
	// first refusal, and stay well inside the drain budget so a gateway that
	// waits for the stream still exits in time.
	const streamPause = 3 * time.Second

	g.dataKey = dev.DataKey
	g.registerMock(t, dev.AdminKey, dev.TenantID, slowUpstream(t, "slow-chat", streamPause), "slow-chat")

	// A client with its own connection pool, warmed so that the request after
	// the signal has an idle connection to reuse.
	transport := &http.Transport{MaxIdleConnsPerHost: 4}
	defer transport.CloseIdleConnections()
	pooled := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	warm, err := pooled.Get(g.URL + "/healthz")
	if err != nil {
		t.Fatalf("warming the connection pool: %v", err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	warm.Body.Close()

	// Open the stream, read the first frame so the request is provably in
	// flight, then signal.
	frames, done := g.openStream(t, pooled, dev.DataKey, "slow-chat")

	signalled := time.Now()
	if err := g.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the gateway: %v", err)
	}

	// The stream runs to completion. `[DONE]` is the sentinel that says the
	// gateway closed it deliberately rather than the socket dying under it.
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the in-flight stream did not finish after SIGTERM: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the in-flight stream never finished after SIGTERM")
	}
	got := <-frames
	// The content written *after* the pause is the evidence, not the sentinel:
	// the gateway appends [DONE] even to a stream it had to abandon, so that a
	// client's read loop still terminates. Only the upstream's later frame shows
	// the drain waited for the request instead of cutting it off.
	if !strings.Contains(got, "there") {
		t.Errorf("the stream lost the frames the upstream sent after SIGTERM, so it was cut off "+
			"rather than completed; frames: %s", truncate([]byte(got)))
	}
	if !strings.Contains(got, "[DONE]") {
		t.Errorf("the stream ended without a [DONE] sentinel; frames: %s", truncate([]byte(got)))
	}

	// A request afterwards is refused. On the pooled connection that is the
	// documented 503; on a connection the kernel refuses it is a transport
	// error. Being served normally is the failure.
	after, err := pooled.Get(g.URL + "/healthz")
	switch {
	case err != nil:
		// Refused at the socket, which is what a closed listener looks like.
	default:
		defer after.Body.Close()
		if after.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("GET /healthz during the drain answered %d, want 503; the probe has to fail "+
				"while draining or a load balancer keeps sending work to a process that is leaving",
				after.StatusCode)
		}
	}

	// And it exits cleanly, inside the budget the criterion names.
	budget := drainTimeout(t) + 5*time.Second
	select {
	case err := <-g.exited:
		g.exited <- err
		if err != nil {
			t.Errorf("the gateway exited %v after SIGTERM, want a clean exit; an orchestrator reads "+
				"a non-zero status as a crash\n%s", err, g.out.String())
		}
		if elapsed := time.Since(signalled); elapsed > budget {
			t.Errorf("the gateway took %s to exit, want at most drain_timeout + 5s (%s)", elapsed, budget)
		}
	case <-time.After(budget):
		t.Fatalf("the gateway had not exited %s after SIGTERM\n%s", budget, g.out.String())
	}
}

// drainTimeout reads shutdown.drain_timeout from the specification's
// configuration table, so the budget this test allows and the budget the product
// documents cannot drift apart.
func drainTimeout(t *testing.T) time.Duration {
	t.Helper()

	spec := repoFile(t, "spec", "gw-11-deployment.md")
	m := regexp.MustCompile(`drain_timeout` + "`?" + `[^\n]*?(\d+)\s*s`).FindStringSubmatch(spec)
	if m == nil {
		t.Fatalf("spec/gw-11-deployment.md does not state a drain_timeout, so this criterion has no " +
			"budget to hold the gateway to")
	}
	seconds, _ := strconv.Atoi(m[1])
	return time.Duration(seconds) * time.Second
}

// openStream sends a streaming completion and reads it on a goroutine, handing
// back the joined frames and the error that ended the read.
func (g *gateway) openStream(t *testing.T, c *http.Client, key, model string) (<-chan string, <-chan error) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"model":    model,
		"stream":   true,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, g.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("opening the stream: status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	// Read the first frame here rather than on the goroutine. The caller signals
	// the process as soon as this returns, and "the response headers arrived"
	// is not the same as "the gateway is streaming": only a frame in hand proves
	// the request is in flight when the signal lands.
	var collected []string
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data:") {
			collected = append(collected, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			break
		}
	}
	if len(collected) == 0 {
		resp.Body.Close()
		t.Fatalf("the stream produced no frame before the first read ended: %v", scanner.Err())
	}

	frames := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		defer resp.Body.Close()
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data:") {
				collected = append(collected, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		frames <- strings.Join(collected, "\n")
		done <- scanner.Err()
	}()
	return frames, done
}

// --- GW-11.AC-6 ---------------------------------------------------------------

// TestGW11_AC6_TheResourceFootprintStaysWithinTheTable measures what the
// specification's table promises: idle RSS and the latency the gateway adds to a
// request it proxies.
//
// Opt-in, because the numbers are hardware. The specification calls this the
// suite's "optional -perf mode" and says the table is informative until CI
// hardware is pinned; a measurement that ran everywhere would either be
// meaningless where it passed or red where the runner was busy. Setting
// CONF_PERF is the operator saying the machine is worth measuring, and on that
// machine the table is enforced.
func TestGW11_AC6_TheResourceFootprintStaysWithinTheTable(t *testing.T) {
	beginOffline(t)

	if os.Getenv("CONF_PERF") == "" {
		t.Skip("set CONF_PERF=1 to measure the resource footprint; the table in " +
			"spec/gw-11-deployment.md is hardware-dependent and informative until CI hardware is pinned")
	}

	g := spawn(t, []string{"--dev"}, map[string]string{
		"ANALYTICS_URL":   "",
		"ANALYTICS_TOKEN": "",
	}, nil)
	dev := g.awaitDevBanner(t)
	g.awaitServing(t)

	mockURL := localMock(t)
	g.dataKey = dev.DataKey
	g.registerMock(t, dev.AdminKey, dev.TenantID, mockURL, "mock-chat-a")

	// Idle RSS, read before any load. Taken from the operating system rather
	// than from the process, because the number the table is about is the one an
	// operator sees in `docker stats`, not one the gateway reports about itself.
	if rss, ok := residentBytes(g.cmd.Process.Pid); !ok {
		t.Logf("idle RSS is not readable on %s; the latency half of the table still applies", runtime.GOOS)
	} else {
		const maxRSS = 64 << 20
		t.Logf("idle RSS: %.1f MiB", float64(rss)/(1<<20))
		if rss > maxRSS {
			t.Errorf("idle RSS is %.1f MiB, above the %d MiB the table allows",
				float64(rss)/(1<<20), maxRSS>>20)
		}
	}

	// Added latency: the same completion through the gateway and straight to the
	// mock, on the same loopback, differenced at the median. A ratio would be
	// dominated by the mock's own service time; the table is written as an
	// addition and this measures the addition.
	const samples = 40
	direct := make([]time.Duration, 0, samples)
	proxied := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		direct = append(direct, timeDirect(t, mockURL))
		start := time.Now()
		if resp := g.chat(t, dev.DataKey, "mock-chat-a"); resp.Status != http.StatusOK {
			t.Fatalf("a completion failed while measuring: status %d\n%s", resp.Status, truncate(resp.Body))
		}
		proxied = append(proxied, time.Since(start))
	}

	added := median(proxied) - median(direct)
	t.Logf("p50 direct %s, p50 proxied %s, added %s", median(direct), median(proxied), added)

	const maxAdded = 5 * time.Millisecond
	if added > maxAdded {
		t.Errorf("the gateway adds %s at p50, above the %s the table allows", added, maxAdded)
	}
}

// timeDirect measures one completion sent straight to the mock, which is the
// baseline the added latency is measured against.
func timeDirect(t *testing.T, mockURL string) time.Duration {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"model":    "mock-chat-a",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	start := time.Now()
	resp, err := http.Post(mockURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("calling the mock directly: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return time.Since(start)
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// residentBytes reads a process's resident set size. Linux only: /proc is where
// the number lives, and the reference deployment is Linux containers. Anywhere
// else this reports that it could not, rather than guessing.
func residentBytes(pid int) (uint64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(body), "\n") {
		rest, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 1 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
