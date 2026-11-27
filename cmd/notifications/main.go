// Command notifications is the composition root of Notifications (Notifications ADR 0002). Its
// subcommand serve runs the event consumer and the send jobs in one process, and serves the
// shared service library's liveness and readiness endpoints over TLS. The subcommand health checks
// readiness for Docker and Compose without a shell, and show, list and recover are the operator
// commands.
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
	"time"
	_ "time/tzdata" // the image FROM scratch has no time-zone database

	"github.com/ancyloce/contelys-cloud-libs-service/config"
	"github.com/ancyloce/contelys-cloud-libs-service/health"
	"github.com/ancyloce/contelys-cloud-libs-service/telemetry"

	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/clients"
	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/clients/identity"
	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/kafka"
	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/mail"
	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/postgres"
)

// slug is the deployable's slug, which names it in its telemetry and to its neighbours.
const slug = "notifications"

// migrationWait is how long an instance waits before it tries its migrations again.
const migrationWait = 2 * time.Second

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout))
}

const usage = "usage: notifications serve|health|show|list|recover -config <file> [arguments]"

// run runs one subcommand and returns the process's exit code. It writes its logs and its answer
// to out.
func run(ctx context.Context, args []string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(out, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], out)
	case "health":
		return checkHealth(ctx, args[1:], out)
	case "show", "list", "recover":
		// The operator commands arrive with ticket T15 of Notifications' plan.
		fmt.Fprintf(out, "notifications %s: not built yet\n", args[0])
		return 2
	default:
		fmt.Fprintln(out, usage)
		return 2
	}
}

// configPath reads the -config flag of a subcommand.
func configPath(name string, args []string, out io.Writer) (string, bool) {
	flags := flag.NewFlagSet(slug+" "+name, flag.ContinueOnError)
	flags.SetOutput(out)
	path := flags.String("config", "", "the configuration file that Ansible renders")
	if err := flags.Parse(args); err != nil || *path == "" {
		fmt.Fprintln(out, usage)
		return "", false
	}
	return *path, true
}

// serve runs until ctx ends or the process receives SIGTERM or SIGINT.
func serve(ctx context.Context, args []string, out io.Writer) int {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()
	path, ok := configPath("serve", args, out)
	if !ok {
		return 2
	}

	// No exporter yet: the spans are dropped until the service sends them to the OpenTelemetry
	// Collector (issue 11 of the phase 1 platform). The logs still carry the trace context.
	logger, shutdown := telemetry.Setup(slug, nil, out)
	defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()

	store, err := config.Open(path, config.Options[settings]{Validate: validate, Live: live})
	if err != nil {
		logger.Error("the configuration was refused", "error", err)
		return 1
	}
	go store.Watch(ctx, time.Second, func(err error) {
		if errors.Is(err, config.ErrRestartRequired) {
			logger.Warn("a changed configuration waits for the next restart", "error", err)
			return
		}
		logger.Error("a changed configuration was refused", "error", err)
	})
	s := store.Current().Values

	pool, err := postgres.Open(ctx, s.Database.URL, s.Database.PasswordFile)
	if err != nil {
		logger.Error("the database cannot be used", "error", err)
		return 1
	}
	defer pool.Close()
	ledger, err := postgres.NewLedger(pool)
	if err != nil {
		logger.Error("the migrations cannot be read", "error", err)
		return 1
	}
	go ledger.Migrate(ctx, migrationWait, func(err error) {
		logger.Warn("the migrations wait", "error", err)
	})

	checks := []health.Check{
		{Name: "database", Required: true, Run: ledger.Readable},
		{Name: "migrations", Required: true, Run: ledger.Migrated},
	}
	signals, err := neighbourSignals(ctx, logger, s)
	if err != nil {
		logger.Error("a neighbour cannot be configured", "error", err)
		return 1
	}
	checks = append(checks, signals...)
	handler := health.Handler(func() (string, string) {
		snapshot := store.Current()
		return snapshot.Revision, snapshot.Digest
	}, checks...)
	return listen(ctx, logger, s, handler)
}

// neighbourSignals are the readiness signals of Kafka, Identity and the mail server, which never
// take the instance out of routing, and the warning of plain SMTP.
func neighbourSignals(ctx context.Context, logger *slog.Logger, s settings) ([]health.Check, error) {
	brokers, err := kafka.Open(s.Kafka.Brokers)
	if err != nil {
		return nil, err
	}
	context.AfterFunc(ctx, brokers.Close)
	transport, err := clients.Transport(s.TLS.Authority)
	if err != nil {
		return nil, err
	}
	identityClient, err := identity.New(s.Internal.URL, transport)
	if err != nil {
		return nil, err
	}
	checks := []health.Check{
		{Name: "kafka", Run: watch(ctx, brokers.Reachable)},
		{Name: "identity", Run: watch(ctx, identityClient.Reachable)},
	}
	if !s.emailEnabled() {
		return checks, nil
	}
	server, err := mailServer(s)
	if err != nil {
		return nil, err
	}
	checks = append(checks,
		health.Check{Name: "mail", Run: watch(ctx, server.Reachable)},
		health.Check{Name: "mail-tls", Run: func(context.Context) error {
			if server.TLS == mail.None {
				return errPlainSMTP
			}
			return nil
		}},
	)
	if server.TLS == mail.None {
		logger.Warn("smtp.tls is none: email and the SMTP password travel unencrypted")
	}
	return checks, nil
}

// errPlainSMTP is the warning signal of smtp.tls: none.
var errPlainSMTP = errors.New("smtp.tls is none")

// mailServer is the configured mail server.
func mailServer(s settings) (mail.Server, error) {
	mode, err := mail.ParseTLSMode(s.SMTP.TLS)
	if err != nil {
		return mail.Server{}, err
	}
	roots, err := mail.Roots(s.SMTP.CA)
	if err != nil {
		return mail.Server{}, err
	}
	return mail.Server{
		Host: s.SMTP.Host, Port: s.SMTP.Port, TLS: mode, Roots: roots,
		Connect: s.SMTP.Timeouts.Connect, Command: s.SMTP.Timeouts.Command,
	}, nil
}

// listen serves handler on the instance's TLS listener until ctx ends.
func listen(ctx context.Context, logger *slog.Logger, s settings, handler http.Handler) int {
	server := &http.Server{Addr: s.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServeTLS(s.TLS.Certificate, s.TLS.Key) }()
	logger.Info("serving")

	select {
	case err := <-served:
		logger.Error("the server stopped", "error", err)
		return 1
	case <-ctx.Done():
	}
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(stopping); err != nil {
		logger.Error("the server did not stop in time", "error", err)
		return 1
	}
	return 0
}
