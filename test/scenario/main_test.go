//go:build scenario

// Package scenario holds Notifications' verification scenarios at the deployable contract seam
// (Notifications ADR 0002, "Test seams"). Each run starts PostgreSQL, Kafka and Mailpit at the test
// baseline on one Docker network, and each scenario starts the image that the image target built,
// with a database of its own. The test acts as the producers and as Edge's internal listener, whose
// fakes of Identity and Organization Access run in this process as Go TLS servers that the container
// reaches through Docker's host gateway. testcontainers-go removes the containers, also when a run
// crashes, and the keys of the throwaway certificate authority live only in memory and inside the
// containers.
package scenario

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ancyloce/contelys-cloud-notifications/test/fixture"
)

// The test baseline, pinned by digest (rule 5 of the plan's section 06 moves it on a security
// release). PostgreSQL is the shared service library's pin.
const (
	postgresImage = "postgres:18-alpine@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71"
	kafkaImage    = "apache/kafka:4.4.0@sha256:833b867a9585a141da455dc2b57431c65cbccb27f11c91f694444cca9f777e0f"
	mailpitImage  = "axllent/mailpit:v1.34.0@sha256:23a2ef85bc50853ab3093645d1d64679afd8887e89da5e48a88940c172591a4e"
)

// The names under which the containers reach each other on the run's network.
const (
	postgresHost = "postgres"
	kafkaHost    = "kafka"
	mailpitHost  = "mailpit"
	// databaseUser owns every scenario's database. Its password reaches Notifications as a file.
	databaseUser     = "notifications"
	databasePassword = "scenario-database-password"
)

// env is the run's environment, which every scenario shares.
var env *environment

type environment struct {
	ca       *fixture.Authority
	network  *testcontainers.DockerNetwork
	postgres *postgres.PostgresContainer
	kafka    *testcontainers.DockerContainer
	mailpit  *testcontainers.DockerContainer
	// admin is the address, from this process, of the database from which each scenario creates
	// its own, and kafkaBrokers that of Kafka.
	admin        string
	kafkaBrokers string
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	e, err := startEnvironment(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start the scenario environment:", err)
		e.stop(ctx)
		os.Exit(1)
	}
	env = e
	code := m.Run()
	e.stop(ctx)
	os.Exit(code)
}

func startEnvironment(ctx context.Context) (*environment, error) {
	e := &environment{}
	var err error
	if e.ca, err = fixture.NewAuthority(); err != nil {
		return e, err
	}
	if e.network, err = network.New(ctx); err != nil {
		return e, fmt.Errorf("network: %w", err)
	}
	e.postgres, err = postgres.Run(ctx, postgresImage,
		postgres.WithDatabase(databaseUser), postgres.WithUsername(databaseUser), postgres.WithPassword(databasePassword),
		postgres.BasicWaitStrategies(), network.WithNetwork([]string{postgresHost}, e.network))
	if err != nil {
		return e, fmt.Errorf("PostgreSQL: %w", err)
	}
	if e.admin, err = e.postgres.ConnectionString(ctx, "sslmode=disable"); err != nil {
		return e, fmt.Errorf("PostgreSQL's address: %w", err)
	}
	if err = e.startKafka(ctx); err != nil {
		return e, fmt.Errorf("Kafka: %w", err)
	}
	if err = e.startMailpit(ctx); err != nil {
		return e, fmt.Errorf("Mailpit: %w", err)
	}
	return e, nil
}

// kafkaAdvertised is where the broker reads the listeners that it advertises, which the test writes
// once it knows the port that Docker mapped for this process.
const kafkaAdvertised = "/tmp/advertised-listeners"

// startKafka starts one broker in KRaft mode with two listeners: INTERNAL, which the containers
// reach as kafka:9092, and EXTERNAL, which this process reaches through its mapped port.
func (e *environment) startKafka(ctx context.Context) error {
	var err error
	e.kafka, err = testcontainers.Run(ctx, kafkaImage,
		network.WithNetwork([]string{kafkaHost}, e.network),
		testcontainers.WithExposedPorts("9093/tcp"),
		testcontainers.WithEnv(map[string]string{
			"KAFKA_NODE_ID":                                  "1",
			"KAFKA_PROCESS_ROLES":                            "broker,controller",
			"KAFKA_LISTENERS":                                "INTERNAL://0.0.0.0:9092,EXTERNAL://0.0.0.0:9093,CONTROLLER://0.0.0.0:9094",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":           "INTERNAL:PLAINTEXT,EXTERNAL:PLAINTEXT,CONTROLLER:PLAINTEXT",
			"KAFKA_INTER_BROKER_LISTENER_NAME":               "INTERNAL",
			"KAFKA_CONTROLLER_LISTENER_NAMES":                "CONTROLLER",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                 "1@localhost:9094",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":            "1",
			"KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS":         "0",
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE":                "false",
		}),
		testcontainers.WithEntrypoint("sh", "-c",
			"while [ ! -f "+kafkaAdvertised+" ]; do sleep 0.1; done; "+
				"export KAFKA_ADVERTISED_LISTENERS=\"$(cat "+kafkaAdvertised+")\"; exec /etc/kafka/docker/run"),
		testcontainers.WithLifecycleHooks(testcontainers.ContainerLifecycleHooks{
			PostStarts: []testcontainers.ContainerHook{func(ctx context.Context, c testcontainers.Container) error {
				if err := wait.ForMappedPort("9093/tcp").WaitUntilReady(ctx, c); err != nil {
					return err
				}
				endpoint, err := c.PortEndpoint(ctx, "9093/tcp", "")
				if err != nil {
					return err
				}
				advertised := "INTERNAL://" + kafkaHost + ":9092,EXTERNAL://" + endpoint
				if err := c.CopyToContainer(ctx, []byte(advertised), kafkaAdvertised, 0o644); err != nil {
					return err
				}
				return wait.ForLog("Kafka Server started").WithStartupTimeout(2*time.Minute).WaitUntilReady(ctx, c)
			}},
		}),
	)
	if err != nil {
		return err
	}
	e.kafkaBrokers, err = e.kafka.PortEndpoint(ctx, "9093/tcp", "")
	return err
}

// startMailpit starts Mailpit with STARTTLS required, from a certificate for the name mailpit that
// the throwaway authority issued.
func (e *environment) startMailpit(ctx context.Context) error {
	certificate, err := e.ca.Issue(mailpitHost)
	if err != nil {
		return err
	}
	e.mailpit, err = testcontainers.Run(ctx, mailpitImage,
		network.WithNetwork([]string{mailpitHost}, e.network),
		testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: bytes.NewReader(certificate.PEM), ContainerFilePath: "/certificates/mailpit.crt", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: bytes.NewReader(certificate.KeyPEM), ContainerFilePath: "/certificates/mailpit.key", FileMode: 0o644},
		),
		testcontainers.WithEnv(map[string]string{
			"MP_SMTP_TLS_CERT":         "/certificates/mailpit.crt",
			"MP_SMTP_TLS_KEY":          "/certificates/mailpit.key",
			"MP_SMTP_REQUIRE_STARTTLS": "true",
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/livez").WithPort("8025/tcp")),
	)
	return err
}

func (e *environment) stop(ctx context.Context) {
	var errs []error
	for _, c := range []testcontainers.Container{e.mailpit, e.kafka} {
		if c != nil && !isNil(c) {
			errs = append(errs, testcontainers.TerminateContainer(c))
		}
	}
	if e.postgres != nil {
		errs = append(errs, testcontainers.TerminateContainer(e.postgres))
	}
	if e.network != nil {
		errs = append(errs, e.network.Remove(ctx))
	}
	if err := errors.Join(errs...); err != nil {
		fmt.Fprintln(os.Stderr, "stop the scenario environment:", err)
	}
}

// isNil tells a typed nil container, which an interface holding it hides.
func isNil(c testcontainers.Container) bool {
	d, ok := c.(*testcontainers.DockerContainer)
	return ok && d == nil
}
