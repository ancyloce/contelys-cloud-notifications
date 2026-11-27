//go:build scenario

package scenario

import (
	"context"
	"testing"
	"time"
)

// Readiness means that the database can be read and the migrations are at the expected version
// (spec, "Deployment and operations"). Both checks take the instance out of routing.
func TestAnInstanceWithItsDatabaseReportsReady(t *testing.T) {
	n := start(t, defaults(t))

	answer := n.ready(t)

	for _, name := range []string{"database", "migrations"} {
		if ok, required := answer.check(t, name); !ok || !required {
			t.Errorf("check %s: ok %v, required %v; want a required check that passes", name, ok, required)
		}
	}
}

// An instance whose database cannot be reached is live and not ready, so that work waits instead
// of the instance being restarted (spec, "Failures").
func TestAnInstanceWithoutItsDatabaseIsNotReady(t *testing.T) {
	n := start(t, defaults(t).set("database.url", "postgres://"+databaseUser+"@absent:5432/notifications?sslmode=disable"))

	answer := n.stays(t, 5*time.Second)

	if answer.Ready {
		t.Fatalf("ready without its database: %+v", answer)
	}
	if ok, _ := answer.check(t, "database"); ok {
		t.Errorf("the database check passed without a database")
	}
}

// An instance whose database is behind the migrations it embeds is not ready, and becomes ready once
// it could apply them. Here its role may not create tables until the test grants it.
func TestAnInstanceIsNotReadyWhileItsMigrationsAreBehind(t *testing.T) {
	s := defaults(t)
	database := s.database()
	role := database + "_role"
	admin(t, database, "CREATE ROLE "+role+" LOGIN PASSWORD '"+databasePassword+"'")
	n := start(t, s.set("database.url", "postgres://"+role+"@"+postgresHost+":5432/"+database+"?sslmode=disable"))

	answer := n.stays(t, 5*time.Second)
	if answer.Ready {
		t.Fatalf("ready although its migrations cannot apply: %+v", answer)
	}
	if ok, _ := answer.check(t, "migrations"); ok {
		t.Fatalf("the migrations check passed before the migrations applied")
	}

	admin(t, database, "GRANT CREATE ON SCHEMA public TO "+role)

	if ok, _ := n.ready(t).check(t, "migrations"); !ok {
		t.Errorf("ready, but the migrations check fails")
	}
}

// The database password reaches the instance as a file (ADR 0020). An instance that starts before
// its file exists becomes ready once the file is there, without a restart.
func TestAnInstanceBecomesReadyWhenItsDatabasePasswordArrives(t *testing.T) {
	n := start(t, defaults(t), secretsDir+"/database-password")
	if answer := n.stays(t, 3*time.Second); answer.Ready {
		t.Fatalf("ready without its database password: %+v", answer)
	}

	if err := n.container.CopyToContainer(context.Background(), []byte(databasePassword), secretsDir+"/database-password", 0o644); err != nil {
		t.Fatal(err)
	}

	n.ready(t)
}
