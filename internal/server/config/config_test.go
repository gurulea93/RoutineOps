package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	os.Clearenv()
	c := Load("non_existent_file.yaml")
	if c.HTTPAddr != ":8081" {
		t.Errorf("expected HTTPAddr :8081, got %s", c.HTTPAddr)
	}
	if c.GRPCAddr != ":50051" {
		t.Errorf("expected GRPCAddr :50051, got %s", c.GRPCAddr)
	}
}

func TestLoad_YAML(t *testing.T) {
	os.Clearenv()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
server:
  http_addr: ":9000"
smtp:
  host: "smtp.example.com"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	c := Load(path)
	if c.HTTPAddr != ":9000" {
		t.Errorf("expected HTTPAddr :9000 from yaml, got %s", c.HTTPAddr)
	}
	if c.SMTPHost != "smtp.example.com" {
		t.Errorf("expected SMTPHost smtp.example.com from yaml, got %s", c.SMTPHost)
	}
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	os.Clearenv()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
server:
  http_addr: ":9000"
`)
	_ = os.WriteFile(path, data, 0600)

	t.Setenv("HTTP_ADDR", ":9001")
	t.Setenv("SMTP_TLS", "true")

	c := Load(path)
	if c.HTTPAddr != ":9001" {
		t.Errorf("expected env HTTP_ADDR :9001 to override yaml, got %s", c.HTTPAddr)
	}
	if !c.SMTPUseTLS {
		t.Error("expected SMTPUseTLS true from env")
	}
}

func TestCoalesce(t *testing.T) {
	if coalesce("", "", "c") != "c" {
		t.Error("coalesce failed, expected c")
	}
	if coalesce("a", "b", "c") != "a" {
		t.Error("coalesce failed, expected a")
	}
	if coalesce() != "" {
		t.Error("coalesce failed, expected empty string")
	}
}
