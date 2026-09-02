package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// SuiteVersion is stamped into conformance-report.json so a stored report says
// which suite produced it. It tracks the specification revision, not the
// gateway under test.
const SuiteVersion = "1.0.0"

// --- configuration ----------------------------------------------------------

// config is the whole environment contract from GW-10. Three variables are
// enough to run the suite; the fourth exists because of where the mock lives.
type config struct {
	// BaseURL is the gateway under test. Empty means "no target", and the
	// suite skips rather than fails: `go test ./...` at the repository root
	// must not go red just because nobody started a gateway.
	BaseURL string

	// AdminKey is a root-scoped admin credential. In the reference deployment
	// that is the bootstrap key, which is not a cga- key — so the suite must
	// not validate its shape, only whether the admin plane accepts it.
	AdminKey string

	// MockProvider is the mock's base URL *as the gateway must dial it*. The
	// literal "embedded" (the default) hosts the mock inside this process,
	// which only works when the suite and the gateway share a host.
	MockProvider string

	// MockControl is the mock's base URL *as the suite must dial it*. It
	// defaults to MockProvider and differs only when the two sides reach the
	// mock by different names — a compose deployment being the case that
	// matters, where the gateway says http://mock-provider:9900 and the runner
	// says http://localhost:9900.
	MockControl string
}

func loadConfig() config {
	c := config{
		BaseURL:      strings.TrimRight(os.Getenv("CONF_BASE_URL"), "/"),
		AdminKey:     os.Getenv("CONF_ADMIN_KEY"),
		MockProvider: os.Getenv("CONF_MOCK_PROVIDER"),
		MockControl:  os.Getenv("CONF_MOCK_CONTROL_URL"),
	}
	if c.MockProvider == "" {
		c.MockProvider = "embedded"
	}
	return c
}

// --- suite state ------------------------------------------------------------

type suiteState struct {
	cfg      config
	client   *client
	features map[string]bool
	version  string

	tenantID  string
	dataKey   string
	mockCtrl  string
	teardown  []func()
	provision error
}

var suite suiteState

// --- HTTP ------------------------------------------------------------------

type client struct {
	base string
	http *http.Client
}

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body, failing the test rather than returning an error: every
// caller would otherwise write the same three lines.
func (r *response) JSON(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("response is not JSON (status %d): %v\n%s", r.Status, err, truncate(r.Body))
	}
	return out
}

// ErrorCode reads error.code out of the envelope every plane answers failures
// in (GW-7). It returns "" when the body has no envelope, so a test can report
// what it actually got instead of panicking.
func (r *response) ErrorCode(t *testing.T) string {
	t.Helper()
	body := r.JSON(t)
	env, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := env["code"].(string)
	return code
}

func (c *client) do(t *testing.T, method, path, key string, body any) *response {
	t.Helper()
	resp, err := c.try(method, path, key, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (c *client) try(method, path, key string, body any) (*response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	raw, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer raw.Body.Close()

	payload, err := io.ReadAll(raw.Body)
	if err != nil {
		return nil, err
	}
	return &response{Status: raw.StatusCode, Header: raw.Header, Body: payload}, nil
}

// admin and data name which credential a call uses, so a test reads as the
// plane it is exercising rather than as a variable lookup.
func (c *client) admin(t *testing.T, method, path string, body any) *response {
	t.Helper()
	return c.do(t, method, path, suite.cfg.AdminKey, body)
}

func (c *client) data(t *testing.T, method, path string, body any) *response {
	t.Helper()
	return c.do(t, method, path, suite.dataKey, body)
}

func truncate(b []byte) string {
	const limit = 512
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "…"
}

// --- the mock ---------------------------------------------------------------

// startMock brings up the mock and returns the URL the gateway should use for
// it. An embedded mock is hosted here; anything else is assumed to be running
// already.
func startMock() (providerURL string) {
	if suite.cfg.MockProvider != "embedded" {
		suite.mockCtrl = strings.TrimRight(suite.cfg.MockControl, "/")
		if suite.mockCtrl == "" {
			suite.mockCtrl = strings.TrimRight(suite.cfg.MockProvider, "/")
		}
		return strings.TrimRight(suite.cfg.MockProvider, "/")
	}

	srv := httptest.NewServer(mockprovider.New().Handler())
	suite.teardown = append(suite.teardown, srv.Close)
	suite.mockCtrl = srv.URL
	return srv.URL
}

// mockControl drives the mock's fault injection. Errors fail the test that
// asked: a control call that did not land means the condition under test was
// never arranged, and continuing would produce a green result for an
// experiment that did not happen.
func mockControl(t *testing.T, method, path string, body any) *response {
	t.Helper()
	ctrl := &client{base: suite.mockCtrl, http: &http.Client{Timeout: 10 * time.Second}}
	resp, err := ctrl.try(method, path, "", body)
	if err != nil {
		t.Fatalf("mock control %s %s: %v", method, path, err)
	}
	if resp.Status >= 300 {
		t.Fatalf("mock control %s %s: status %d\n%s", method, path, resp.Status, truncate(resp.Body))
	}
	return resp
}

// injectFault arranges an upstream condition and clears it when the test ends,
// so one test's fault can never leak into the next.
func injectFault(t *testing.T, model, mode string, count int) {
	t.Helper()
	mockControl(t, http.MethodPost, "/_control/faults", map[string]any{
		"model": model, "mode": mode, "count": count,
	})
	t.Cleanup(func() {
		mockControl(t, http.MethodPost, "/_control/faults", map[string]any{
			"model": model, "mode": mockprovider.FaultNone,
		})
	})
}

// addMockModel registers a model that belongs to this test alone and removes it
// afterwards. Tests that need to fault a model use one of these rather than a
// seed model, so two concurrent suite runs cannot disturb each other
// (GW-10.AC-3).
func addMockModel(t *testing.T, id string) string {
	t.Helper()
	mockControl(t, http.MethodPost, "/_control/models", map[string]any{
		"id": id, "context_window": 128000, "max_output_tokens": 4096,
	})
	t.Cleanup(func() {
		mockControl(t, http.MethodDelete, "/_control/models/"+id, nil)
	})
	return id
}

// --- provisioning -----------------------------------------------------------

// provision creates everything this run owns: one tenant, one data key, one
// provider pointing at the mock. Names carry the run id so a second suite
// running against the same deployment collides with nothing.
func provision(runID string) error {
	c := suite.client

	tenant, err := c.try(http.MethodPost, "/admin/v1/tenants", suite.cfg.AdminKey,
		map[string]any{"name": "conformance-" + runID})
	if err != nil {
		return fmt.Errorf("creating the tenant: %w", err)
	}
	if tenant.Status != http.StatusCreated {
		return fmt.Errorf("creating the tenant: status %d\n%s\n"+
			"CONF_ADMIN_KEY must be a root-scoped admin credential; in the reference "+
			"deployment that is the value of GATEWAY_BOOTSTRAP_KEY", tenant.Status, truncate(tenant.Body))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(tenant.Body, &created); err != nil || created.ID == "" {
		return fmt.Errorf("the created tenant has no id: %s", truncate(tenant.Body))
	}
	suite.tenantID = created.ID

	key, err := c.try(http.MethodPost, "/admin/v1/tenants/"+created.ID+"/keys", suite.cfg.AdminKey,
		map[string]any{"name": "conformance", "plane": "data"})
	if err != nil {
		return fmt.Errorf("minting a data key: %w", err)
	}
	var minted struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(key.Body, &minted); err != nil || minted.Secret == "" {
		return fmt.Errorf("minting a data key: status %d, no secret in %s", key.Status, truncate(key.Body))
	}
	suite.dataKey = minted.Secret

	providerURL := startMock()
	prov, err := c.try(http.MethodPost, "/admin/v1/tenants/"+created.ID+"/providers", suite.cfg.AdminKey,
		map[string]any{
			"name":     "mock",
			"kind":     "openai",
			"base_url": providerURL,
			// Two keys so a test can prove the gateway rotates within a
			// provider's pool before it gives up on the provider (GW-3).
			"keys": []string{"mock-key-primary", "mock-key-secondary"},
		})
	if err != nil {
		return fmt.Errorf("registering the mock provider: %w", err)
	}
	if prov.Status != http.StatusCreated {
		return fmt.Errorf("registering the mock provider: status %d\n%s", prov.Status, truncate(prov.Body))
	}

	return awaitCatalog(providerURL)
}

// awaitCatalog is the fail-fast the whole harness turns on.
//
// The URL the gateway dials for the mock is not the URL this process dials, and
// getting that wrong is the single easiest way to misconfigure a run. Left
// undetected it surfaces as forty unrelated tests failing on 503, so it is
// worth one explicit check with a message that names the actual cause.
func awaitCatalog(providerURL string) error {
	deadline := time.Now().Add(30 * time.Second)
	var last string

	for time.Now().Before(deadline) {
		resp, err := suite.client.try(http.MethodGet, "/v1/models", suite.dataKey, nil)
		if err != nil {
			last = err.Error()
		} else {
			var body struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(resp.Body, &body); err == nil {
				for _, m := range body.Data {
					if strings.Contains(m.ID, "mock-chat-a") {
						return nil
					}
				}
			}
			last = fmt.Sprintf("status %d: %s", resp.Status, truncate(resp.Body))
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf(
		"the gateway never listed the mock's models, so it cannot reach the mock at %s.\n"+
			"CONF_MOCK_PROVIDER must be the mock's address *as the gateway dials it* — for a\n"+
			"containerised gateway that is a service name on its network, not a localhost port\n"+
			"on this machine. Set CONF_MOCK_CONTROL_URL when the suite reaches it by a different\n"+
			"name. Last response: %s", providerURL, last)
}

// deprovision removes everything provision created and reports whether the
// deployment is genuinely clean afterwards (GW-10.AC-4).
func deprovision() error {
	for i := len(suite.teardown) - 1; i >= 0; i-- {
		suite.teardown[i]()
	}
	if suite.tenantID == "" {
		return nil
	}

	del, err := suite.client.try(http.MethodDelete, "/admin/v1/tenants/"+suite.tenantID, suite.cfg.AdminKey, nil)
	if err != nil {
		return fmt.Errorf("deleting the tenant: %w", err)
	}
	if del.Status != http.StatusNoContent && del.Status != http.StatusNotFound {
		return fmt.Errorf("deleting the tenant: status %d\n%s", del.Status, truncate(del.Body))
	}

	// Deleting is not the same as being gone. AC-4 is about what remains.
	check, err := suite.client.try(http.MethodGet, "/admin/v1/tenants/"+suite.tenantID, suite.cfg.AdminKey, nil)
	if err != nil {
		return fmt.Errorf("verifying the tenant is gone: %w", err)
	}
	if check.Status != http.StatusNotFound {
		return fmt.Errorf("the suite's tenant %s still exists after teardown (status %d)",
			suite.tenantID, check.Status)
	}
	return nil
}
