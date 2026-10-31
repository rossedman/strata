package crawl

import (
	"reflect"
	"testing"

	"github.com/rossedman/strata/internal/model"
)

func TestDockerPURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"pkg:docker/node@20?platform=linux%2Famd64":                               "docker.io/library/node:20",
		"pkg:docker/bitnami/redis@7":                                              "docker.io/bitnami/redis:7",
		"pkg:docker/111111111111.dkr.ecr.us-east-1.amazonaws.com/base/node@20":    "111111111111.dkr.ecr.us-east-1.amazonaws.com/base/node:20",
		"pkg:docker/gcr.io%2Fdistroless%2Fstatic@nonroot":                         "gcr.io/distroless/static:nonroot",
		"pkg:docker/node?repository_url=public.ecr.aws%2Fdocker%2Flibrary%2Fnode": "public.ecr.aws/docker/library/node",
		"pkg:docker/alpine@sha256:abc":                                            "docker.io/library/alpine",
		"pkg:docker/localhost:5000/base/os@1":                                     "localhost:5000/base/os:1",
	} {
		got, ok := dockerPURL(in)
		if !ok || got != want {
			t.Errorf("dockerPURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"pkg:npm/left-pad@1", "pkg:docker/", "pkg:docker/%zz", "pkg:docker/UPPER CASE@1"} {
		if got, ok := dockerPURL(bad); ok {
			t.Errorf("dockerPURL(%q) = %q, want rejected", bad, got)
		}
	}
}

func TestParseProvenance(t *testing.T) {
	t.Parallel()
	v02 := `{"predicateType":"https://slsa.dev/provenance/v0.2","predicate":{"materials":[
		{"uri":"pkg:docker/docker/dockerfile@1","digest":{"sha256":"f"}},
		{"uri":"pkg:docker/golang@1.22?platform=linux%2Famd64","digest":{"sha256":"a"}},
		{"uri":"pkg:docker/golang@1.22?platform=linux%2Famd64","digest":{"sha256":"a"}},
		{"uri":"https://github.com/acme/app.git#main","digest":{"sha1":"c"}},
		{"uri":"pkg:docker/gcr.io/distroless/static@nonroot","digest":{"sha256":"b"}}]}}`
	want := []model.BaseRef{
		{Ref: "docker.io/library/golang:1.22", BaseDigest: "sha256:a"},
		{Ref: "gcr.io/distroless/static:nonroot", BaseDigest: "sha256:b"},
	}
	if got := parseProvenance([]byte(v02)); !reflect.DeepEqual(got, want) {
		t.Fatalf("v0.2 = %+v", got)
	}
	v1 := `{"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"resolvedDependencies":[
		{"uri":"pkg:docker/node@20","digest":{"sha256":"d"}},{"uri":"pkg:docker/alpine@3"}]}}}`
	want = []model.BaseRef{{Ref: "docker.io/library/node:20", BaseDigest: "sha256:d"}, {Ref: "docker.io/library/alpine:3"}}
	if got := parseProvenance([]byte(v1)); !reflect.DeepEqual(got, want) {
		t.Fatalf("v1 = %+v", got)
	}
	if got := parseProvenance([]byte("not json")); got != nil {
		t.Fatalf("bad JSON = %+v", got)
	}
	if got := (model.BaseRef{Ref: "docker.io/library/node:20", BaseDigest: "sha256:d"}).FetchRef(); got != "docker.io/library/node@sha256:d" {
		t.Fatalf("FetchRef = %s", got)
	}
	if got := (model.BaseRef{Ref: "localhost:5000/base/os"}).FetchRef(); got != "localhost:5000/base/os" {
		t.Fatalf("FetchRef without digest = %s", got)
	}
}
