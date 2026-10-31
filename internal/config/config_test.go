package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		{Config{DB: "x", Registries: []Registry{{Host: "h", Provider: "gcr"}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "h", Provider: ProviderECR}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "h", ExcludeTags: []string{"[x"}}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "h", Platforms: []string{"linux"}}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "h", PushedSince: "90d"}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: ecrHost, PushedSince: "3parsecs"}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: ecrHost, PushedSince: "90d", IncludeRepos: []string{"team-*/**"}, Platforms: []string{"linux/amd64"}}}}, true},
		{Config{DB: "x", Registries: []Registry{{Host: "us-docker.pkg.dev"}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: ecrHost, AWSProfile: "base", AssumeRole: "arn:aws:iam::1:role/r", ExternalID: "x"}}}, true},
		{Config{DB: "x", Registries: []Registry{{Host: ecrHost, ExternalID: "x"}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "reg.io", AWSProfile: "base"}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "us-docker.pkg.dev", Projects: []string{"acme"}, PushedSince: "1y"}}}, true},
		{Config{DB: "x", Registries: []Registry{{Host: "reg.io", Provider: ProviderGAR, Projects: []string{"acme"}}}}, false},
		{Config{DB: "x", Registries: []Registry{{Host: "harbor.io", Provider: ProviderHarbor, PushedSince: "30d"}}}, true},
		{Config{DB: "x", Registries: []Registry{{Host: "acme.azurecr.io", PushedSince: "90d"}}}, true},
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

const ecrHost = "123456789012.dkr.ecr.us-east-1.amazonaws.com"

func TestRegistryProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		r    Registry
		want bool
	}{
		{Registry{Host: ecrHost}, true},
		{Registry{Host: ecrHost, Provider: ProviderAuto}, true},
		{Registry{Host: ecrHost, Provider: ProviderDistribution}, false},
		{Registry{Host: ecrHost, Provider: ProviderECR}, true},
		{Registry{Host: "reg.io"}, false},
	}
	for _, tt := range tests {
		if got := tt.r.UsesECR(); got != tt.want {
			t.Errorf("%+v: UsesECR = %v", tt.r, got)
		}
	}
	for _, k := range []struct {
		r    Registry
		want string
	}{
		{Registry{Host: ecrHost}, ProviderECR},
		{Registry{Host: "europe-west1-docker.pkg.dev"}, ProviderGAR},
		{Registry{Host: "acme.azurecr.io"}, ProviderACR},
		{Registry{Host: "harbor.io"}, ProviderDistribution},
		{Registry{Host: "harbor.io", Provider: ProviderHarbor}, ProviderHarbor},
		{Registry{Host: "acme.azurecr.io", Provider: ProviderDistribution}, ProviderDistribution},
	} {
		if got := k.r.Kind(); got != k.want {
			t.Errorf("%s/%s: Kind = %s, want %s", k.r.Host, k.r.Provider, got, k.want)
		}
	}
	f, err := Registry{Host: ecrHost, PushedSince: "2w", ExcludeTags: []string{"pr-*"}}.Filter()
	if err != nil || f.PushedSince != 14*24*time.Hour || len(f.ExcludeTags) != 1 {
		t.Fatalf("filter = %+v, %v", f, err)
	}
}
