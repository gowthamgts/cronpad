package deploy_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDeploymentYAML(t *testing.T) {
	files := []string{"docker-compose.yml", "docker-compose.seaweedfs.yml", "litestream.yml", "otel-collector.yml", "prometheus.yml", "prometheus/alerts.yml", "../.goreleaser.yaml", "../.github/workflows/ci.yml", "../.github/workflows/release.yml"}
	k8s, err := filepath.Glob("kubernetes/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, k8s...)
	provisioning, err := filepath.Glob("grafana/provisioning/*/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, provisioning...)
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			f, e := os.Open(file)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = f.Close() }()
			decoder := yaml.NewDecoder(f)
			for {
				var doc map[string]any
				e = decoder.Decode(&doc)
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				if len(doc) == 0 {
					t.Fatal("empty YAML document")
				}
			}
		})
	}
}
