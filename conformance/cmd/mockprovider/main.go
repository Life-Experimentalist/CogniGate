// Command mockprovider runs the conformance suite's fake upstream as a process.
//
// The suite can host the mock in-process (CONF_MOCK_PROVIDER=embedded), but
// that only works when the suite and the gateway share a network namespace. A
// gateway in a container cannot dial an ephemeral port on the test runner, so
// against a compose deployment the mock has to be a service of its own — which
// is what this binary is for.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

func main() {
	port := flag.Int("port", 9900, "port to listen on")
	flag.Parse()

	srv := &http.Server{
		Addr:    mockprovider.ListenAddr(*port),
		Handler: mockprovider.New().Handler(),
		// No write timeout: a timeout fault is meant to stall for longer than
		// the gateway will wait, and a server-side deadline would cut it short
		// and turn the stall into a response.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("mock provider listening on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("mock provider: %v", err)
	}
}
