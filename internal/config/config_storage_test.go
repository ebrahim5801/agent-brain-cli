package config

import (
	"os"
	"strings"
	"testing"
)

func TestStorageRoundTrip(t *testing.T) {
	setDir(t)
	in := &Config{Storage: &Storage{
		Backend: "postgres",
		DSN:     "postgres://brain:secret@localhost:5432/agentbrain?sslmode=disable",
	}}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.Storage == nil {
		t.Fatal("storage section lost on round-trip")
	}
	if out.Storage.Backend != in.Storage.Backend || out.Storage.DSN != in.Storage.DSN {
		t.Errorf("storage round-trip mismatch: got %+v want %+v", out.Storage, in.Storage)
	}
}

func TestStorageAbsentByDefault(t *testing.T) {
	setDir(t)
	if err := Save(&Config{}); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "storage") {
		t.Errorf("empty config serialized a storage section:\n%s", data)
	}
	out, _ := Load()
	if out.Storage != nil {
		t.Errorf("absent storage section loaded as non-nil: %+v", out.Storage)
	}
}

func TestSaveWithStorageIsPrivate(t *testing.T) {
	setDir(t)
	if err := Save(&Config{Storage: &Storage{Backend: "postgres", DSN: "postgres://u:p@h/db"}}); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config holding a DSN has perms %v, want 0600", fi.Mode().Perm())
	}
}

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name, in string
		wantNo   string // substring that must NOT appear (the password)
		wantHas  string // substring that must appear
	}{
		{"url", "postgres://brain:secret@localhost:5432/db?sslmode=disable", "secret", "brain"},
		{"url no password", "postgres://brain@localhost:5432/db", "", "brain"},
		{"postgresql scheme", "postgresql://u:topsecret@h/db", "topsecret", "u"},
		{"keyword", "host=localhost user=brain password=secret dbname=agentbrain", "secret", "user=brain"},
		{"keyword spaced", "host=localhost password = secret dbname=x", "secret", "host=localhost"},
		{"empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactDSN(c.in)
			if c.wantNo != "" && strings.Contains(got, c.wantNo) {
				t.Errorf("RedactDSN(%q) = %q still contains the password %q", c.in, got, c.wantNo)
			}
			if c.wantHas != "" && !strings.Contains(got, c.wantHas) {
				t.Errorf("RedactDSN(%q) = %q dropped %q", c.in, got, c.wantHas)
			}
			if c.in != "" && !strings.Contains(got, "****") && c.wantNo != "" {
				t.Errorf("RedactDSN(%q) = %q did not insert a mask", c.in, got)
			}
		})
	}
}
