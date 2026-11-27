//go:build scenario

package scenario

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// The image holds Notifications' binary and the public root certificates alone, with the bundle's
// licence and the name and version of its Debian package (Notifications ADR 0002), and runs under a
// numeric user that is not root.
func TestTheImageHoldsTheBinaryAndTheRootCertificatesAlone(t *testing.T) {
	ctx := context.Background()
	docker, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()

	inspected, err := docker.ImageInspect(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	if user := inspected.Config.User; user == "" || user == "0" || strings.HasPrefix(user, "0:") || strings.HasPrefix(user, "root") {
		t.Errorf("the image runs as %q, want a numeric user that is not root", user)
	}
	if check := inspected.Config.Healthcheck; check == nil || !slices.Contains(check.Test, "health") {
		t.Errorf("the image's health check is %+v, want the health subcommand", check)
	}

	files := imageFiles(t, docker, image)
	want := []string{
		"etc/ssl/certs/ca-certificates.crt",
		"notifications",
		"usr/share/doc/ca-certificates/copyright",
		"usr/share/doc/ca-certificates/package",
	}
	if !slices.Equal(files, want) {
		t.Errorf("the image holds %q, want %q", files, want)
	}
}

// The health subcommand answers as readiness does, so that Docker and Compose check an instance
// without a shell.
func TestTheHealthSubcommandAnswersAsReadinessDoes(t *testing.T) {
	healthy := start(t, defaults(t))
	healthy.ready(t)
	unhealthy := start(t, defaults(t).set("database.url", "postgres://"+databaseUser+"@absent:5432/notifications?sslmode=disable"))

	if code := healthy.health(t); code != 0 {
		t.Errorf("health of a ready instance exited %d, want 0", code)
	}
	if code := unhealthy.health(t); code == 0 {
		t.Errorf("health of an instance without its database exited 0")
	}
}

// health runs the health subcommand inside the instance's container, as its health check does.
func (n *instance) health(t *testing.T) int {
	t.Helper()
	code, _, err := n.container.Exec(context.Background(), []string{"/notifications", "health", "-config", configDir + "/config.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// imageFiles lists the regular files of the image's layers, sorted, as docker save writes them.
func imageFiles(t *testing.T, docker *testcontainers.DockerClient, name string) []string {
	t.Helper()
	saved, err := docker.ImageSave(context.Background(), []string{name})
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	archive := map[string][]byte{}
	reader := tar.NewReader(saved)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if archive[header.Name], err = io.ReadAll(reader); err != nil {
				t.Fatal(err)
			}
		}
	}
	var manifest []struct {
		Layers []string `json:"Layers"`
	}
	if err := json.Unmarshal(archive["manifest.json"], &manifest); err != nil || len(manifest) != 1 {
		t.Fatalf("docker save wrote no manifest of one image: %v", err)
	}
	var files []string
	for _, layer := range manifest[0].Layers {
		reader := tar.NewReader(strings.NewReader(string(archive[layer])))
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("layer %s: %v", layer, err)
			}
			if header.Typeflag == tar.TypeReg {
				files = append(files, strings.TrimPrefix(header.Name, "./"))
			}
		}
	}
	slices.Sort(files)
	return files
}
