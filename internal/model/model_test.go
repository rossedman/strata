package model

import "testing"

func TestPlatformString(t *testing.T) {
	t.Parallel()
	tests := map[Platform]string{
		{}:                           "",
		{OS: "linux", Arch: "amd64"}: "linux/amd64",
		{OS: "linux", Arch: "arm", Variant: "v7"}: "linux/arm/v7",
	}
	for p, want := range tests {
		if got := p.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", p, got, want)
		}
	}
}

func TestImageHelpers(t *testing.T) {
	t.Parallel()
	img := &Image{
		Labels:      map[string]string{"team": "label", "a": "1"},
		Annotations: map[string]string{"team": "annotation", "b": "2"},
	}
	if img.TopChainID() != "" {
		t.Fatal("empty image has a top chain ID")
	}
	img.ChainIDs = []string{"x", "y"}
	if img.TopChainID() != "y" {
		t.Fatalf("TopChainID = %s", img.TopChainID())
	}
	m := img.Metadata()
	if m["team"] != "label" || m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("Metadata = %v", m)
	}
	if (Tag{Repo: "r", Tag: "t"}).Ref() != "r:t" {
		t.Fatal("Tag.Ref")
	}
	if !IsPhantom(PhantomPrefix+"x") || IsPhantom("sha256:x") {
		t.Fatal("IsPhantom")
	}
}

func TestClassifyArtifact(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"application/spdx+json":                            KindSBOM,
		"application/vnd.cyclonedx+json":                   KindSBOM,
		"application/vnd.dev.cosign.simplesigning.v1+json": KindSignature,
		"application/vnd.cncf.notary.signature":            KindSignature,
		"application/vnd.in-toto+json":                     KindAttestation,
		"application/vnd.dsse.envelope.v1+json":            KindAttestation,
		"application/vnd.oci.image.manifest.v1+json":       KindOther,
	}
	for in, want := range tests {
		if got := ClassifyArtifact(in); got != want {
			t.Errorf("ClassifyArtifact(%q) = %s, want %s", in, got, want)
		}
	}
}
