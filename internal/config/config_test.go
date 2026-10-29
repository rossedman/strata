package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDB(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if got := DefaultDB(); got != filepath.Join("/data", "strata", "strata.duckdb") {
		t.Fatalf("DefaultDB = %s", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/me")
	if got := DefaultDB(); got != filepath.Join("/home/me", ".local", "share", "strata", "strata.duckdb") {
		t.Fatalf("DefaultDB = %s", got)
	}
	t.Setenv("HOME", "")
	if got := DefaultDB(); !strings.HasSuffix(got, "strata.duckdb") {
		t.Fatalf("DefaultDB without home = %s", got)
	}
	if Defaults()["referrers"] != true {
		t.Fatal("referrers default")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		c  Config
		ok bool
	}{
		{Config{DB: "x"}, true},
		{Config{}, false},
		{Config{DB: "x", Registries: []Registry{{}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "h", Workers: -1}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "h", Workers: 4, Rate: 10}}}, true},
	}
	for i, tt := range tests {
		if err := tt.c.Validate(); (err == nil) != tt.ok {
			t.Errorf("case %d: Validate = %v", i, err)
		}
	}
	c := Config{}
	if p, err := c.Policy(); err != nil || p.Thresholds.StaleGenerations != 2 {
		t.Fatalf("default policy = %+v, %v", p, err)
	}
	c.PolicyFile = "/nope.yaml"
	if _, err := c.Policy(); err == nil {
		t.Fatal("missing policy file accepted")
	}
}
