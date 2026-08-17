package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/config"
)

// GW-11 is the deployment story. What is testable in this package is the half
// the process itself owns: what a drain does to an arriving request, and
// whether a configured keypair actually produces an HTTPS listener. The compose
// file, the dev banner and the resource envelope are properties of a
// deployment rather than of a Server, and are asserted by the conformance
// suite against a running one.

// --- GW-11.AC-5: drain ------------------------------------------------------

// TestDrainRefusesArrivingRequests pins the drain contract: 503 with
// Connection: close, on both planes.
//
// It drives the middleware through Draining rather than through a real
// Shutdown, because Shutdown closes the listener and app.Test would then have
// nothing to send to — the state under test is "draining and still reachable",
// which is precisely the window a keep-alive client sits in.
func TestDrainRefusesArrivingRequests(t *testing.T) {
	h := newHarness(t)
	dataKey := h.newTenant("drain").dataKey

	// Healthy first, so the refusal below is attributable to the drain and not
	// to a request that was going to fail anyway.
	if res := h.do(http.MethodGet, "/v1/models", dataKey, nil); res.status != http.StatusOK {
		t.Fatalf("before draining: GET /v1/models = %d, want 200", res.status)
	}

	h.srv.draining.Store(true)

	for _, tc := range []struct {
		path  string
		token string
	}{
		{"/v1/models", dataKey},
		{"/v1/chat/completions", dataKey},
		{"/admin/v1/tenants", testBootstrapKey},
		// An unknown path too. A caller who cannot even be told "no such
		// endpoint" because the process is going away is still owed the reason
		// it got no answer, and 404 would send them looking for a typo.
		{"/v1/nonesuch", dataKey},
	} {
		res := h.do(http.MethodGet, tc.path, tc.token, nil)
		h.expectError(res, http.StatusServiceUnavailable, apierr.CodeUnavailable)
		if !res.close {
			t.Errorf("%s during drain did not answer Connection: close. Without it "+
				"a client keeps reusing a socket the gateway is about to close.", tc.path)
		}
	}

	// And the negative, so the assertion above is known to be discriminating:
	// a healthy gateway must not be telling every client to drop its
	// connection, which would defeat keep-alive on the whole data plane.
	h.srv.draining.Store(false)
	if res := h.do(http.MethodGet, "/v1/models", dataKey, nil); res.close {
		t.Error("a healthy gateway answered Connection: close")
	}
}

// TestDrainLeavesHealthzAndMetricsAnswering guards the two carve-outs.
//
// Both exist to describe the drain. /healthz must keep answering its own 503
// so a load balancer reads "draining" rather than a generic envelope, and
// /metrics must keep serving so the drain is observable while it happens.
// Folding either into the blanket refusal would be invisible in a 503-counting
// test, which is why they are asserted on their bodies.
func TestDrainLeavesHealthzAndMetricsAnswering(t *testing.T) {
	h := newHarness(t)
	h.srv.draining.Store(true)

	res := h.do(http.MethodGet, "/healthz", "", nil)
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz during drain = %d, want 503", res.status)
	}
	var health struct {
		Status string `json:"status"`
	}
	res.decode(t, &health)
	if health.Status != "draining" {
		t.Errorf(`GET /healthz during drain reported status %q, want "draining". `+
			"A load balancer steers on this body, not on the status alone.", health.Status)
	}

	res = h.do(http.MethodGet, "/metrics", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET /metrics during drain = %d, want 200: a gateway that stops "+
			"reporting when it starts draining is dark for the interval worth watching", res.status)
	}
	if !strings.Contains(string(res.body), "cognigate_") {
		t.Errorf("GET /metrics during drain returned no cognigate_ series:\n%s", res.body)
	}
}

// TestShutdownSetsDrainingBeforeItReturns is the wiring check behind AC-5.
//
// The signal handler in main does one thing — call Shutdown — and this is the
// half of that path a test can own on every platform. SIGTERM is deliberately
// not delivered here: it is not deliverable on Windows, and a test that only
// runs on some of the machines the gateway is developed on would let this
// regress silently on the others.
func TestShutdownSetsDrainingBeforeItReturns(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Shutdown.DrainTimeout = 2 * time.Second
	})

	if h.srv.Draining() {
		t.Fatal("a freshly built server reports itself draining")
	}

	done := make(chan error, 1)
	go func() { done <- h.srv.Shutdown(t.Context()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return within 10s on a server with nothing in flight")
	}

	if !h.srv.Draining() {
		t.Error("Draining is false after Shutdown returned; /healthz would keep " +
			"reporting ok while the process was going away")
	}
}

// --- GW-11.AC-4: TLS --------------------------------------------------------

// TestListenServesHTTPSWithACertificate is the end-to-end half of AC-4: a real
// listener on a real port, reached over TLS, and refusing plaintext.
//
// The keypair is generated here rather than read from a fixture. A committed
// PEM private key is indistinguishable from a leaked one to every secret
// scanner that will ever look at this repository, and the test needs a
// certificate, not a particular certificate.
func TestListenServesHTTPSWithACertificate(t *testing.T) {
	certPEM, keyPEM := selfSignedLoopbackCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("the generated keypair does not load: %v", err)
	}

	h := newHarness(t)
	h.srv.TLSCertificate = &cert

	addr := listenOnFreePort(t, h)

	client := &http.Client{
		Transport: &http.Transport{
			// The certificate is self-signed and generated seconds ago, so it
			// is pinned by value rather than trusted by chain: the assertion is
			// that this listener presented this certificate, which is stronger
			// than skipping verification would be.
			TLSClientConfig: &tls.Config{RootCAs: poolOf(t, certPEM), ServerName: "localhost"},
		},
		Timeout: 5 * time.Second,
	}

	res, err := client.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("HTTPS request to a TLS-configured listener failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET https://.../healthz = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"ok"`) {
		t.Errorf("healthz over TLS returned %q", body)
	}

	// Plaintext to the same port. The listener speaks TLS, so the handshake
	// fails and there is no HTTP response at all — which is what "rejects
	// plaintext" means for a TLS listener, as opposed to a redirect.
	plain := &http.Client{Timeout: 5 * time.Second}
	if res, err := plain.Get("http://" + addr + "/healthz"); err == nil {
		defer res.Body.Close()
		t.Errorf("plaintext HTTP to the TLS listener was answered with %d; "+
			"GW-11.AC-4 requires it to be rejected", res.StatusCode)
	}
}

// TestListenIsPlaintextWithoutACertificate is the other half of the branch. It
// exists so that a change making TLS unconditional — or making it silently the
// default — fails here rather than in a deployment whose reverse proxy suddenly
// cannot reach its own gateway.
func TestListenIsPlaintextWithoutACertificate(t *testing.T) {
	h := newHarness(t)
	if h.srv.TLSCertificate != nil {
		t.Fatal("the default harness configured TLS")
	}

	addr := listenOnFreePort(t, h)

	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("plaintext request to a listener with no certificate failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET http://.../healthz = %d, want 200", res.StatusCode)
	}
}

// --- configuration ----------------------------------------------------------

// TestTLSConfigurationMustBeComplete pins the half-configured case as a startup
// error. A deployment that set only the certificate believes it is serving
// HTTPS; falling back to plaintext would leave it doing the opposite of what
// its configuration says, and doing so silently.
func TestTLSConfigurationMustBeComplete(t *testing.T) {
	for _, tc := range []struct {
		name string
		cert string
		key  string
		ok   bool
	}{
		{"neither", "", "", true},
		{"both", "cert.pem", "key.pem", true},
		{"certificate only", "cert.pem", "", false},
		{"key only", "", "key.pem", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Gateway.TLSCertFile = tc.cert
			cfg.Gateway.TLSKeyFile = tc.key

			err := cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("Validate accepted a half-configured TLS pair")
			}
			if got := cfg.Gateway.TLSEnabled(); got != (tc.cert != "" && tc.key != "") {
				t.Errorf("TLSEnabled() = %v for cert=%q key=%q", got, tc.cert, tc.key)
			}
		})
	}
}

// --- helpers ----------------------------------------------------------------

// listenOnFreePort starts the server on a port the OS picked and returns the
// address. The listener is closed when the test ends.
//
// The port is discovered by opening and immediately closing one, rather than by
// hardcoding a number: two packages' tests running concurrently on one machine
// would otherwise collide, and the failure would look like a bug in TLS.
func listenOnFreePort(t *testing.T, h *harness) string {
	t.Helper()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	errs := make(chan error, 1)
	go func() { errs <- h.srv.Listen(addr) }()
	t.Cleanup(func() {
		if err := h.srv.Shutdown(t.Context()); err != nil {
			t.Errorf("shutting the listener down: %v", err)
		}
		<-errs
	})

	// Wait for the port to answer rather than sleeping a fixed interval: a
	// fixed sleep is either slower than it needs to be or flaky on a loaded
	// machine, and here it would be both.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return addr
		}
		select {
		case err := <-errs:
			t.Fatalf("Listen(%s) returned before the port answered: %v", addr, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the listener on %s did not accept a connection within 5s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// selfSignedLoopbackCert mints a certificate for localhost, valid for an hour.
//
// Generated per-run and never written to the repository. The key material a
// test needs exists for the length of the test; a committed one would live in
// the history forever and would be a finding in every scan of it.
func selfSignedLoopbackCert(t *testing.T) (certPEM, keyPEM []byte) {
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
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the key: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// poolOf builds a root pool holding exactly the given certificate, so a test
// verifies against that certificate rather than skipping verification.
func poolOf(t *testing.T, certPEM []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("the generated certificate is not a PEM certificate")
	}
	return pool
}

// writeKeypair puts a generated keypair on disk and returns the two paths, for
// the tests that go through configuration rather than through a *tls.Certificate.
func writeKeypair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	certPEM, keyPEM := selfSignedLoopbackCert(t)
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("writing the certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	return certFile, keyFile
}

// TestConfiguredKeypairLoads closes the gap between the configuration surface
// GW-11 documents and the *tls.Certificate the server takes: the paths an
// operator sets have to produce a certificate the listener accepts.
func TestConfiguredKeypairLoads(t *testing.T) {
	certFile, keyFile := writeKeypair(t)

	cfg := config.Default()
	cfg.Gateway.TLSCertFile = certFile
	cfg.Gateway.TLSKeyFile = keyFile
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !cfg.Gateway.TLSEnabled() {
		t.Fatal("TLSEnabled() is false with both paths set")
	}
	if _, err := tls.LoadX509KeyPair(cfg.Gateway.TLSCertFile, cfg.Gateway.TLSKeyFile); err != nil {
		t.Fatalf("loading the configured keypair: %v", err)
	}

	// And the failing direction, which is the one an operator actually hits:
	// a path that is not a certificate must be an error, not a plaintext
	// listener.
	bogus := filepath.Join(t.TempDir(), "not-a-cert.pem")
	if err := os.WriteFile(bogus, []byte("this is not a certificate\n"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(bogus, keyFile); err == nil {
		t.Error("a file that is not a certificate loaded as one")
	}
}
