package main

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ancyloce/contelys-cloud-notifications/internal/application/retention"
	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/mail"
	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/postgres"
)

// settings is Notifications' configuration file, which Ansible renders: the keys of the spec's
// table, and those that reach the database, Kafka, Edge's internal listener and the instance's own
// TLS listener. Secrets are files that the settings name (ADR 0020).
type settings struct {
	// Listen is the address of the TLS listener that serves the health endpoints.
	Listen string `yaml:"listen"`
	TLS    struct {
		Certificate string `yaml:"certificate"`
		Key         string `yaml:"key"`
		// Authority is the installation's certificate authority, which verifies Edge's internal
		// listener (ADR 0039).
		Authority string `yaml:"authority"`
	} `yaml:"tls"`
	Database struct {
		// URL is a PostgreSQL URL without a password.
		URL          string `yaml:"url"`
		PasswordFile string `yaml:"password_file"`
	} `yaml:"database"`
	Kafka struct {
		Brokers []string `yaml:"brokers"`
	} `yaml:"kafka"`
	Internal struct {
		// URL is the address of Edge's internal listener, under which each neighbour's slug
		// names its operations.
		URL string `yaml:"url"`
	} `yaml:"internal"`
	SMTP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		// TLS is starttls, implicit or, only when set explicitly, none.
		TLS      string `yaml:"tls"`
		CA       string `yaml:"ca"`
		From     string `yaml:"from"`
		Timeouts struct {
			Connect time.Duration `yaml:"connect"`
			Command time.Duration `yaml:"command"`
			Content time.Duration `yaml:"content"`
		} `yaml:"timeouts"`
	} `yaml:"smtp"`
	Email struct {
		// Enabled is required, so that email is never switched off by a missing key.
		Enabled *bool `yaml:"enabled"`
	} `yaml:"email"`
	Send struct {
		Paused      bool `yaml:"paused"`
		Concurrency int  `yaml:"concurrency"`
		Attempts    int  `yaml:"attempts"`
		Backoff     struct {
			First   time.Duration `yaml:"first"`
			Longest time.Duration `yaml:"longest"`
		} `yaml:"backoff"`
	} `yaml:"send"`
	Links struct {
		Base string `yaml:"base"`
	} `yaml:"links"`
	Retention struct {
		PersonalData time.Duration `yaml:"personalData"`
		Identities   time.Duration `yaml:"identities"`
	} `yaml:"retention"`
}

// live lists the settings that change without a restart (spec, "Configuration"). Every other
// setting waits for a rolling restart.
var live = []string{"send.paused", "send.attempts", "send.backoff", "retention.personalData", "retention.identities"}

// validate is Notifications' own check of a configuration file, beside the library's. It runs when
// the file is applied, at start and at each live change.
func validate(s settings) error {
	var problems []string
	require := func(ok bool, problem string) {
		if !ok {
			problems = append(problems, problem)
		}
	}
	require(s.Listen != "", "listen is required")
	require(s.TLS.Certificate != "" && s.TLS.Key != "" && s.TLS.Authority != "", "tls.certificate, tls.key and tls.authority are required")
	require(s.Database.URL != "" && s.Database.PasswordFile != "", "database.url and database.password_file are required")
	if err := postgres.CheckURL(s.Database.URL); err != nil {
		problems = append(problems, err.Error())
	}
	require(len(s.Kafka.Brokers) > 0, "kafka.brokers is required")
	require(isHTTPS(s.Internal.URL), "internal.url must be an https URL")
	require(s.Email.Enabled != nil, "email.enabled is required")
	if s.emailEnabled() {
		require(s.SMTP.Host != "" && s.SMTP.Port > 0 && s.SMTP.Port < 65536, "smtp.host and smtp.port are required")
		if _, err := mail.ParseTLSMode(s.SMTP.TLS); err != nil {
			problems = append(problems, err.Error())
		}
		require(s.SMTP.From != "", "smtp.from is required")
		require(s.SMTP.Timeouts.Connect > 0 && s.SMTP.Timeouts.Command > 0 && s.SMTP.Timeouts.Content > 0, "smtp.timeouts.connect, command and content must be positive")
	}
	require(s.Send.Concurrency > 0, "send.concurrency must be positive")
	require(s.Send.Attempts > 0, "send.attempts must be positive")
	require(s.Send.Backoff.First > 0 && s.Send.Backoff.Longest >= s.Send.Backoff.First, "send.backoff.first must be positive and send.backoff.longest at least as long")
	require(isHTTPS(s.Links.Base), "links.base must be an https URL")
	require(s.Retention.PersonalData > 0, "retention.personalData must be positive")
	if err := retention.CheckIdentities(s.Retention.Identities); err != nil {
		problems = append(problems, "retention.identities: "+err.Error())
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// emailEnabled reports whether the installation sends email.
func (s settings) emailEnabled() bool {
	return s.Email.Enabled != nil && *s.Email.Enabled
}

// isHTTPS reports whether raw is an absolute https URL with a host.
func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}
