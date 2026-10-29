// Command demo-registry runs an in-memory OCI registry seeded with a
// realistic organization's images, so strata can be tried without access to
// a real registry:
//
//	go run ./examples/demo-registry -addr 127.0.0.1:5000
//	strata index --registry 127.0.0.1:5000 --insecure
//
// POST /_demo/advance rebuilds the bases (debian, go, go-service), pushes a
// new payments/api on top, and deletes a tag, so a second index run records
// tag moves and base drift:
//
//	curl -X POST 127.0.0.1:5000/_demo/advance
//	strata index --registry 127.0.0.1:5000 --insecure
//
// Layers are small deterministic blobs, so sizes are kilobytes rather than
// what real images weigh.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// demo pushes images to the registry at host.
type demo struct {
	host   string
	rng    *rand.ChaCha8
	images map[string]v1.Image
	phase  int
	mu     sync.Mutex
}

// main starts the registry and seeds it.
func main() {
	addr := flag.String("addr", "127.0.0.1:5000", "listen address")
	flag.Parse()
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)), registry.WithReferrersSupport(true))
	d := &demo{host: *addr, rng: rand.NewChaCha8([32]byte{42}), images: map[string]v1.Image{}}
	mux := http.NewServeMux()
	mux.Handle("/", reg)
	mux.HandleFunc("POST /_demo/advance", func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if err := d.advance(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, "advanced to phase %d\n", d.phase)
	})
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		time.Sleep(200 * time.Millisecond)
		d.mu.Lock()
		defer d.mu.Unlock()
		if err := d.seed(); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "demo registry ready on %s with %d images\n", *addr, len(d.images))
	}()
	log.Fatal(srv.ListenAndServe())
}

// day returns a creation time days after 2025-01-01.
func day(n int) time.Time {
	return time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

// layer returns a deterministic pseudo-random layer of size bytes.
func (d *demo) layer(size int) v1.Layer {
	b := make([]byte, size)
	_, _ = d.rng.Read(b) // deterministic: the same seed gives the same demo
	return static.NewLayer(b, types.OCILayer)
}

// spec describes one image to build.
type spec struct {
	key     string
	base    string
	created int
	steps   []string
	sizes   []int
	labels  map[string]string
	env     []string
	user    string
	ports   []string
	health  bool
	entry   []string
	arch    string
}

// build creates an image from a spec and remembers it under its key.
func (d *demo) build(s spec) (v1.Image, error) {
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	if s.base != "" {
		img = d.images[s.base]
	}
	for i, step := range s.steps {
		size := 4096
		if i < len(s.sizes) {
			size = s.sizes[i]
		}
		var err error
		img, err = mutate.Append(img, mutate.Addendum{Layer: d.layer(size), History: v1.History{CreatedBy: step, Created: v1.Time{Time: day(s.created)}}})
		if err != nil {
			return nil, err
		}
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture = "linux", "amd64"
	if s.arch != "" {
		cf.Architecture = s.arch
	}
	cf.Created = v1.Time{Time: day(s.created)}
	labels := map[string]string{}
	for k, v := range cf.Config.Labels {
		labels[k] = v
	}
	for k, v := range s.labels {
		labels[k] = v
	}
	cf.Config.Labels = labels
	cf.Config.Env = append(append([]string{}, cf.Config.Env...), s.env...)
	if s.user != "" {
		cf.Config.User = s.user
	}
	if len(s.entry) > 0 {
		cf.Config.Entrypoint = s.entry
	}
	if len(s.ports) > 0 {
		cf.Config.ExposedPorts = map[string]struct{}{}
		for _, p := range s.ports {
			cf.Config.ExposedPorts[p] = struct{}{}
		}
	}
	if s.health {
		cf.Config.Healthcheck = &v1.HealthConfig{Test: []string{"CMD", "/healthz"}}
	}
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		return nil, err
	}
	d.images[s.key] = img
	return img, nil
}

// push writes an image to repo:tag.
func (d *demo) push(ref string, img v1.Image) error {
	r, err := name.ParseReference(d.host+"/"+ref, name.Insecure)
	if err != nil {
		return err
	}
	return remote.Write(r, img)
}

// ship builds an image and pushes it under each tag.
func (d *demo) ship(s spec, repo string, tags ...string) error {
	img, err := d.build(s)
	if err != nil {
		return fmt.Errorf("%s: %w", s.key, err)
	}
	for _, t := range tags {
		if err := d.push(repo+":"+t, img); err != nil {
			return fmt.Errorf("%s:%s: %w", repo, t, err)
		}
	}
	return nil
}

// app returns the labels most services carry.
func app(team, env, source string) map[string]string {
	return map[string]string{"team": team, "env": env, "org.opencontainers.image.source": "https://github.com/acme/" + source}
}

// seed pushes the first state of the registry.
func (d *demo) seed() error {
	steps := []struct {
		s    spec
		repo string
		tags []string
	}{
		{spec{key: "debian", created: 0, steps: []string{"ADD rootfs.tar.xz / # debian bookworm-slim"}, sizes: []int{30000}}, "mirror/debian", []string{"bookworm-slim"}},
		{spec{key: "buster", created: -400, steps: []string{"ADD rootfs.tar.xz / # debian buster-slim"}, sizes: []int{28000}}, "mirror/debian", []string{"buster-slim"}},
		{spec{key: "alpine", created: 10, steps: []string{"ADD alpine-minirootfs.tar.gz /"}, sizes: []int{8000}}, "mirror/alpine", []string{"3.20"}},
		{spec{key: "go121", base: "debian", created: 20, steps: []string{"RUN apt-get install -y ca-certificates git", "RUN curl -fsSL https://go.dev/dl/go1.21.13.linux-amd64.tar.gz | tar -C /usr/local -xz"}, sizes: []int{9000, 60000}, env: []string{"GO_VERSION=1.21.13"}}, "base/go", []string{"1.21"}},
		{spec{key: "go122", base: "debian", created: 40, steps: []string{"RUN apt-get install -y ca-certificates git", "RUN curl -fsSL https://go.dev/dl/go1.22.6.linux-amd64.tar.gz | tar -C /usr/local -xz"}, sizes: []int{9000, 62000}, env: []string{"GO_VERSION=1.22.6"}}, "base/go", []string{"1.22"}},
		{spec{key: "gosvc", base: "go122", created: 45, steps: []string{"RUN useradd -u 10001 app && mkdir /app"}, sizes: []int{2000}, labels: map[string]string{"team": "platform"}, user: "10001"}, "base/go-service", []string{"2.1"}},
		{spec{key: "node", base: "debian", created: 30, steps: []string{"RUN curl -fsSL https://nodejs.org/dist/v20.15.1/node-v20.15.1-linux-x64.tar.xz | tar -xJ"}, sizes: []int{45000}, env: []string{"NODE_VERSION=20.15.1"}}, "base/node", []string{"20"}},
		{spec{key: "python", base: "debian", created: 35, steps: []string{"RUN apt-get install -y python3.12 python3-pip"}, sizes: []int{40000}, env: []string{"PYTHON_VERSION=3.12.4"}}, "base/python", []string{"3.12"}},
		{spec{key: "python-dup", base: "debian", created: 90, steps: []string{"RUN apt-get install -y python3.12 python3-pip python3-venv"}, sizes: []int{41000}, labels: map[string]string{"Team": "data"}}, "data/python-base", []string{"3.12"}},

		{spec{key: "api-180", base: "gosvc", created: 60, steps: []string{"COPY api /app/api"}, labels: app("payments", "prod", "payments-api"), env: []string{"PORT=8080"}, ports: []string{"8080/tcp"}, health: true, entry: []string{"/app/api"}}, "payments/api", []string{"1.8.0"}},
		{spec{key: "api-182", base: "gosvc", created: 95, steps: []string{"COPY api /app/api"}, labels: app("payments", "prod", "payments-api"), env: []string{"PORT=8080", "AWS_SECRET_ACCESS_KEY=redacted-at-source"}, ports: []string{"8080/tcp"}, health: true, entry: []string{"/app/api"}}, "payments/api", []string{"1.8.2", "latest"}},
		{spec{key: "worker", base: "gosvc", created: 96, steps: []string{"COPY worker /app/worker"}, labels: app("payments", "prod", "payments-worker"), entry: []string{"/bin/sh", "-c", "/app/start-worker.sh"}}, "payments/worker", []string{"3.2.0", "latest"}},
		{spec{key: "ledger", base: "go121", created: 15, steps: []string{"COPY ledger /usr/local/bin/ledger", "RUN TOKEN=s3cr3t ./migrate --all"}, labels: app("payments", "prod", "ledger"), user: "root"}, "payments/ledger", []string{"0.9.4"}},
		{spec{key: "refunds", base: "gosvc", created: 50, steps: []string{"COPY refunds /app/refunds"}, labels: map[string]string{"owner": "payments", "env": "staging"}}, "payments/refunds", []string{"0.3.0"}},

		{spec{key: "site", base: "node", created: 70, steps: []string{"COPY dist /srv/site", "RUN npm ci --omit=dev"}, sizes: []int{12000, 38000}, labels: app("web", "prod", "site"), ports: []string{"3000/tcp"}, health: true}, "web/site", []string{"4.1.0", "latest"}},
		{spec{key: "admin", base: "node", created: 72, steps: []string{"COPY dist /srv/admin", "RUN npm ci --omit=dev"}, sizes: []int{9000, 36000}, labels: app("web", "prod", "admin"), ports: []string{"3000/tcp", "22/tcp"}}, "web/admin", []string{"2.0.3"}},
		{spec{key: "docs", base: "node", created: 25, steps: []string{"COPY site /srv/docs"}, labels: map[string]string{"team": "web", "env": "prod", "deprecated": "true"}}, "web/docs", []string{"1.0.0"}},

		{spec{key: "etl", base: "python", created: 80, steps: []string{"COPY requirements.txt /app/", "RUN pip install -r /app/requirements.txt", "COPY etl /app/etl"}, sizes: []int{1000, 90000, 6000}, labels: app("data", "prod", "etl")}, "data/etl", []string{"2024.11", "latest"}},
		{spec{key: "scheduler", base: "python", created: 82, steps: []string{"COPY scheduler /app/"}, labels: map[string]string{"team-name": "data", "env": "prod"}}, "data/scheduler", []string{"1.4.0"}},
		{spec{key: "notebook", base: "python-dup", created: 92, steps: []string{"RUN pip install torch jupyterlab pandas"}, sizes: []int{250000}, labels: map[string]string{"Team": "data", "env": "dev"}}, "data/notebook", []string{"latest"}},

		{spec{key: "gateway", base: "gosvc", created: 100, steps: []string{"COPY gateway /app/gateway"}, labels: app("platform", "prod", "gateway"), ports: []string{"443/tcp"}, health: true}, "platform/gateway", nil},
		{spec{key: "auth", base: "alpine", created: 85, steps: []string{"RUN apk add --no-cache ca-certificates", "COPY auth /usr/bin/auth"}, labels: app("platform", "prod", "auth"), env: []string{"GITHUB_TOKEN=x"}}, "platform/auth", []string{"5.0.1"}},

		{spec{key: "cron", base: "buster", created: -300, steps: []string{"RUN apt-get install -y cron curl", "COPY jobs /etc/cron.d/"}, labels: map[string]string{"team": "legacy"}}, "legacy/cron", []string{"stable"}},
		{spec{key: "billing", created: -200, steps: []string{"ADD billing-squashed.tar /"}, sizes: []int{50000}, labels: map[string]string{"team": "legacy"}}, "legacy/billing", []string{"1.0"}},
	}
	for _, st := range steps {
		if err := d.ship(st.s, st.repo, st.tags...); err != nil {
			return err
		}
	}
	// A static binary built FROM scratch, traceable only by its annotation.
	metrics, err := d.build(spec{key: "metrics", created: 98, steps: []string{"COPY metrics /metrics"}, labels: app("platform", "prod", "metrics")})
	if err != nil {
		return err
	}
	metrics = mutate.Annotations(metrics, map[string]string{"org.opencontainers.image.base.name": d.host + "/base/go:1.22"}).(v1.Image)
	if err := d.push("platform/metrics:1.0.0", metrics); err != nil {
		return err
	}
	// The gateway is multi-platform.
	arm, err := d.build(spec{key: "gateway-arm", base: "alpine", created: 100, steps: []string{"COPY gateway-arm64 /app/gateway"}, labels: app("platform", "prod", "gateway"), arch: "arm64"})
	if err != nil {
		return err
	}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: d.images["gateway"], Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	ref, err := name.ParseReference(d.host+"/platform/gateway:7.2.0", name.Insecure)
	if err != nil {
		return err
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		return err
	}
	return d.attach("api-182")
}

// attach pushes an SBOM referrer and a cosign-style signature for an image.
func (d *demo) attach(key string) error {
	img := d.images[key]
	dg, err := img.Digest()
	if err != nil {
		return err
	}
	size, err := img.Size()
	if err != nil {
		return err
	}
	sbom := mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), "application/spdx+json")
	sbom = mutate.Subject(sbom, v1.Descriptor{MediaType: types.OCIManifestSchema1, Digest: dg, Size: size}).(v1.Image)
	sd, err := sbom.Digest()
	if err != nil {
		return err
	}
	if err := d.push("payments/api@"+sd.String(), sbom); err != nil {
		return err
	}
	sig := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	r, err := name.ParseReference(d.host+"/payments/api:sha256-"+dg.Hex+".sig", name.Insecure)
	if err != nil {
		return err
	}
	return remote.Write(r, sig)
}

// advance rebuilds the bases and moves tags, as a week of builds would.
func (d *demo) advance() error {
	d.phase++
	c := 120 + d.phase*10
	if err := d.ship(spec{key: "debian", created: c, steps: []string{"ADD rootfs.tar.xz / # debian bookworm-slim, security update"}, sizes: []int{30100}}, "mirror/debian", "bookworm-slim"); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if err := d.ship(spec{key: "go122", base: "debian", created: c + i, steps: []string{"RUN apt-get install -y ca-certificates git", "RUN curl -fsSL https://go.dev/dl/go1.22.7.linux-amd64.tar.gz | tar -C /usr/local -xz"}, sizes: []int{9000, 62100}, env: []string{"GO_VERSION=1.22.7"}}, "base/go", "1.22"); err != nil {
			return err
		}
	}
	if err := d.ship(spec{key: "gosvc", base: "go122", created: c + 2, steps: []string{"RUN useradd -u 10001 app && mkdir /app"}, labels: map[string]string{"team": "platform"}, user: "10001"}, "base/go-service", "2.1"); err != nil {
		return err
	}
	if err := d.ship(spec{key: "api-183", base: "gosvc", created: c + 3, steps: []string{"COPY api /app/api"}, labels: app("payments", "prod", "payments-api"), ports: []string{"8080/tcp"}, health: true, entry: []string{"/app/api"}}, "payments/api", "1.8.3", "latest"); err != nil {
		return err
	}
	r, err := name.ParseReference(d.host+"/payments/api:1.8.0", name.Insecure)
	if err != nil {
		return err
	}
	return remote.Delete(r)
}
