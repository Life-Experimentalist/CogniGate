// Command cognigate is the CogniGate edge gateway: an OpenAI-compatible data
// plane on /v1 and an administrative plane on /admin/v1, in one process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/obs"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags)"
//
// The default is semver rather than the word "dev" because /v1/meta publishes it
// as a version a client is entitled to compare (GW-9), and an unparseable one is
// worse than an honest 0.0.0. The prerelease segment is what says "not a
// release": it sorts below every released version, which is exactly right for a
// build nobody tagged.
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "cognigate: %v\n", err)
		os.Exit(1)
	}
}

// run is main with the process boundary passed in, so a test can drive the
// whole startup path — flags, configuration, assembly — without exiting.
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cognigate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath  = fs.String("config", "", "path to a configuration file; defaults are used when unset")
		dev         = fs.Bool("dev", false, "run a self-contained development server: in-memory storage, no external dependencies, and printed throwaway keys")
		showVersion = fs.Bool("version", false, "print the version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: cognigate [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *dev {
		applyDevDefaults(&cfg)
	}

	logger := obs.NewLogger(cfg.Log.Level)

	a, err := build(cfg, *dev, logger, version)
	if err != nil {
		return err
	}
	defer a.Close()

	if a.dev != nil {
		printDevBanner(stdout, cfg, a.dev)
	}

	return serve(a, logger)
}

// serve runs the listener until a signal arrives, then drains.
func serve(a *app, logger *slog.Logger) error {
	// Registered before the listener starts so a signal arriving during startup
	// is not lost.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := fmt.Sprintf(":%d", a.cfg.Gateway.Port)
	errs := make(chan error, 1)
	go func() {
		logger.Info("listening",
			slog.String("addr", addr),
			slog.String("version", version),
			slog.String("store", a.store.Kind()))
		// A closed server is an ordinary shutdown, not a failure to report.
		if err := a.server.Listen(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	// From here /healthz fails, so an orchestrator stops routing new work,
	// while requests already in flight — open SSE streams included — run to
	// completion or hit the drain timeout.
	logger.Info("draining", slog.Duration("timeout", a.cfg.Shutdown.DrainTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Shutdown.DrainTimeout)
	defer cancel()

	if err := a.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("draining: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// applyDevDefaults fills in what a dev process needs and an operator should not
// have to supply. Only values that would otherwise stop the server from
// starting, and only when they were not set explicitly.
func applyDevDefaults(cfg *config.Config) {
	if cfg.Admin.BootstrapKey == "" {
		// A dev process still needs a bootstrap credential for the admin plane
		// to be reachable before the seeded key is used. It is fixed rather than
		// random so it can be written down in the documentation, and it is
		// unmistakably a dev value.
		cfg.Admin.BootstrapKey = devBootstrapKey
	}
}

// devBootstrapKey is the admin bootstrap credential of a `--dev` process. It is
// not a secret and must never be a deployment's: the "dev" infix is there so
// that a copy of it in a production configuration is obvious on sight, and the
// dev banner says as much.
const devBootstrapKey = "cga-dev-bootstrap-do-not-deploy"

func printDevBanner(w io.Writer, cfg config.Config, creds *devCredentials) {
	fmt.Fprintf(w, `
CogniGate development server
  Nothing is persisted. Every key below is discarded when this process exits.

  Tenant       %s
  Data key     %s
  Admin key    %s
  Bootstrap    %s

  Data plane   http://localhost:%d/v1
  Admin plane  http://localhost:%d/admin/v1

  Register a provider on the admin plane before sending a completion; the
  catalog is empty until one is configured.

`,
		creds.TenantID, creds.DataKey, creds.AdminKey, devBootstrapKey,
		cfg.Gateway.Port, cfg.Gateway.Port)
}
