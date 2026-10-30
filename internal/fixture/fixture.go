// Package fixture builds synthetic images and indexes for tests, so
// lineage, staleness and query tests can describe a registry in a few
// lines instead of crawling one.
package fixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/chain"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

// Digest returns a deterministic sha256 digest for name.
func Digest(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Epoch is the reference time fixtures are created relative to.
var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Image builds a linux/amd64 image in repo whose layers are named by
// layers; equal names give equal diff IDs. name makes the digest unique.
func Image(name, repo string, layers ...string) *model.Image {
	img := &model.Image{
		Digest:       Digest("manifest:" + name),
		Repo:         repo,
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		Platform:     model.Platform{OS: "linux", Arch: "amd64"},
		Created:      Epoch,
		ConfigDigest: Digest("config:" + name),
		Labels:       map[string]string{},
		Annotations:  map[string]string{},
	}
	diffIDs := make([]string, len(layers))
	for i, l := range layers {
		diffIDs[i] = Digest("layer:" + l)
		img.Layers = append(img.Layers, model.Layer{
			DiffID:     diffIDs[i],
			BlobDigest: Digest("blob:" + l),
			Size:       int64(1000 * (i + 1)),
			MediaType:  "application/vnd.oci.image.layer.v1.tar+gzip",
			CreatedBy:  "RUN step " + l,
		})
		img.Size += int64(1000 * (i + 1))
		img.History = append(img.History, model.HistoryEntry{CreatedBy: "RUN step " + l})
	}
	ids, err := chain.IDs(diffIDs)
	if err != nil {
		panic(err)
	}
	img.ChainIDs = ids
	return img
}

// Created sets the image's creation time to Epoch plus days.
func Created(img *model.Image, days int) *model.Image {
	img.Created = Epoch.AddDate(0, 0, days)
	return img
}

// Label sets a label on the image.
func Label(img *model.Image, k, v string) *model.Image {
	img.Labels[k] = v
	return img
}

// Tag points repo:tag at the image, at Epoch plus days.
func Tag(img *model.Image, tag string, days int) model.Tag {
	return model.Tag{Repo: img.Repo, Tag: tag, Digest: img.Digest, UpdatedAt: Epoch.AddDate(0, 0, days)}
}

// Event records repo:tag pointing at digest at Epoch plus days.
func Event(repo, tag, digest string, days int) model.TagEvent {
	return model.TagEvent{Repo: repo, Tag: tag, Digest: digest, SeenAt: Epoch.AddDate(0, 0, days), Source: "crawl"}
}

// Open returns an in-memory store holding batch, closed when the test
// ends.
func Open(t testing.TB, b *store.Batch) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if b != nil {
		if err := s.Write(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
