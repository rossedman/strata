package ecr

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/rossedman/strata/internal/model"
)

const host = "123456789012.dkr.ecr.us-east-1.amazonaws.com"

// stubManifest is the manifest the stub ECR endpoint returns.
const stubManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{}}`

func TestParseHost(t *testing.T) {
	t.Parallel()
	tests := map[string][2]string{
		host: {"123456789012", "us-east-1"},
		"123456789012.dkr.ecr-fips.us-gov-west-1.amazonaws.com": {"123456789012", "us-gov-west-1"},
		"123456789012.dkr.ecr.cn-north-1.amazonaws.com.cn":      {"123456789012", "cn-north-1"},
	}
	for h, want := range tests {
		account, region, ok := ParseHost(h)
		if !ok || account != want[0] || region != want[1] {
			t.Errorf("ParseHost(%q) = %q, %q, %v", h, account, region, ok)
		}
	}
	for _, h := range []string{"reg.io", "public.ecr.aws", "12345.dkr.ecr.us-east-1.amazonaws.com", host + ".evil.com"} {
		if _, _, ok := ParseHost(h); ok {
			t.Errorf("ParseHost(%q) should fail", h)
		}
	}
}

// fakeAPI serves canned ECR pages.
type fakeAPI struct {
	repos     [][]types.Repository
	images    [][]types.ImageDetail
	manifests map[string]string
	findings  []*types.ImageScanFindings
	asked     [][]string
	tokens    int
	token     string
	err       error
}

func (f *fakeAPI) BatchGetImage(_ context.Context, in *ecr.BatchGetImageInput, _ ...func(*ecr.Options)) (*ecr.BatchGetImageOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &ecr.BatchGetImageOutput{}
	var asked []string
	for _, id := range in.ImageIds {
		d := aws.ToString(id.ImageDigest)
		asked = append(asked, d)
		if m, ok := f.manifests[d]; ok {
			out.Images = append(out.Images, types.Image{ImageId: &types.ImageIdentifier{ImageDigest: id.ImageDigest}, ImageManifest: aws.String(m)})
		} else {
			out.Failures = append(out.Failures, types.ImageFailure{ImageId: &types.ImageIdentifier{ImageDigest: id.ImageDigest}, FailureCode: types.ImageFailureCodeImageNotFound})
		}
	}
	f.asked = append(f.asked, asked)
	return out, nil
}

func (f *fakeAPI) DescribeRepositories(_ context.Context, in *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	i := page(in.NextToken)
	out := &ecr.DescribeRepositoriesOutput{Repositories: f.repos[i]}
	if i+1 < len(f.repos) {
		out.NextToken = aws.String(string(rune('0' + i + 1)))
	}
	return out, nil
}

func (f *fakeAPI) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	i := page(in.NextToken)
	out := &ecr.DescribeImagesOutput{ImageDetails: f.images[i]}
	if i+1 < len(f.images) {
		out.NextToken = aws.String(string(rune('0' + i + 1)))
	}
	return out, nil
}

func (f *fakeAPI) DescribeImageScanFindings(_ context.Context, in *ecr.DescribeImageScanFindingsInput, _ ...func(*ecr.Options)) (*ecr.DescribeImageScanFindingsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	i := page(in.NextToken)
	out := &ecr.DescribeImageScanFindingsOutput{ImageScanFindings: f.findings[i]}
	if i+1 < len(f.findings) {
		out.NextToken = aws.String(string(rune('0' + i + 1)))
	}
	return out, nil
}

func (f *fakeAPI) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.tokens++
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []types.AuthorizationData{{
		AuthorizationToken: aws.String(f.token),
		ExpiresAt:          aws.Time(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
	}}}, nil
}

// page turns a fake next token into a page number.
func page(tok *string) int {
	if tok == nil {
		return 0
	}
	return int((*tok)[0] - '0')
}

func TestReposAndImages(t *testing.T) {
	t.Parallel()
	pushed := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	api := &fakeAPI{
		repos: [][]types.Repository{{{RepositoryName: aws.String("a")}}, {{RepositoryName: aws.String("b/c")}}},
		images: [][]types.ImageDetail{
			{{ImageDigest: aws.String("sha256:1"), ImageTags: []string{"v1", "latest"}, ImageManifestMediaType: aws.String("application/vnd.oci.image.manifest.v1+json"),
				ImagePushedAt: aws.Time(pushed), LastRecordedPullTime: aws.Time(pushed.Add(time.Hour))}},
			{
				{ImageDigest: aws.String("sha256:2"), ImageStatus: types.ImageStatusArchived},
				{ImageDigest: aws.String("sha256:3"), SubjectManifestDigest: aws.String("sha256:1"), ArtifactMediaType: aws.String("application/spdx+json")},
			},
		},
	}
	r := NewWithAPI(context.Background(), host, "123456789012", api)
	repos, err := r.Repos(context.Background())
	if err != nil || strings.Join(repos, ",") != "a,b/c" {
		t.Fatalf("repos = %v, %v", repos, err)
	}
	images, err := r.Images(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("archived images should be left out: %+v", images)
	}
	if i := images[0]; i.Digest != "sha256:1" || len(i.Tags) != 2 || !i.PushedAt.Equal(pushed) || !i.LastPulled.Equal(pushed.Add(time.Hour)) {
		t.Fatalf("image = %+v", i)
	}
	if i := images[1]; i.Subject != "sha256:1" || i.ArtifactType != "application/spdx+json" || !i.PushedAt.IsZero() {
		t.Fatalf("artifact = %+v", i)
	}

	api.err = errors.New("AccessDenied")
	if _, err := r.Repos(context.Background()); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Images(context.Background(), "a"); err == nil || !strings.Contains(err.Error(), "describe images in a") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeychain(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{token: base64.StdEncoding.EncodeToString([]byte("AWS:secret"))}
	r := NewWithAPI(context.Background(), host, "123456789012", api)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }

	other, err := r.Resolve(mustRepo(t, "docker.io/library/debian"))
	if err != nil || other != authn.Anonymous {
		t.Fatalf("other hosts should be anonymous: %v, %v", other, err)
	}
	auth, err := r.Resolve(mustRepo(t, host+"/payments/api"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		cfg, err := auth.Authorization()
		if err != nil || cfg.Username != "AWS" || cfg.Password != "secret" {
			t.Fatalf("auth = %+v, %v", cfg, err)
		}
	}
	if api.tokens != 1 {
		t.Fatalf("token fetched %d times, want once while it is fresh", api.tokens)
	}
	// Within ten minutes of expiry the token is fetched again.
	now = time.Date(2026, 1, 1, 11, 55, 0, 0, time.UTC)
	if _, err := auth.Authorization(); err != nil || api.tokens != 2 {
		t.Fatalf("tokens = %d, %v", api.tokens, err)
	}

	for _, tok := range []string{"%%%", base64.StdEncoding.EncodeToString([]byte("nocolon"))} {
		bad := NewWithAPI(context.Background(), host, "123456789012", &fakeAPI{token: tok})
		if _, err := bad.token(); err == nil {
			t.Errorf("token %q should fail", tok)
		}
	}
	if _, err := NewWithAPI(context.Background(), host, "1", &fakeAPI{err: errors.New("no creds")}).token(); err == nil {
		t.Error("a failed token request should fail")
	}
}

func mustRepo(t *testing.T, s string) name.Repository {
	t.Helper()
	r, err := name.NewRepository(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestNew runs the real SDK client against a stub ECR endpoint.
func TestNew(t *testing.T) {
	t.Parallel()
	if _, err := New(context.Background(), "reg.io"); err == nil {
		t.Fatal("a non-ECR host should fail")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		target := req.Header.Get("X-Amz-Target")
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch {
		case strings.HasSuffix(target, ".DescribeRepositories"):
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"repositoryName": "payments/api"}}})
		case strings.HasSuffix(target, ".BatchGetImage"):
			var in struct {
				AcceptedMediaTypes []string `json:"acceptedMediaTypes"`
			}
			_ = json.NewDecoder(req.Body).Decode(&in)
			if len(in.AcceptedMediaTypes) != 4 {
				http.Error(w, "accepted media types missing", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"images": []map[string]any{{
				"imageId": map[string]any{"imageDigest": digestOf(stubManifest)}, "imageManifest": stubManifest,
			}}, "failures": []any{}})
		case strings.HasSuffix(target, ".DescribeImages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"imageDetails": []map[string]any{{
				"imageDigest": "sha256:1", "imageTags": []string{"v1"}, "imagePushedAt": 1764547200,
				"imageScanStatus":          map[string]any{"status": "ACTIVE"},
				"imageScanFindingsSummary": map[string]any{"imageScanCompletedAt": 1764550800, "findingSeverityCounts": map[string]int{"CRITICAL": 2, "UNTRIAGED": 1}},
			}}})
		case strings.HasSuffix(target, ".DescribeImageScanFindings"):
			_ = json.NewEncoder(w).Encode(map[string]any{"imageScanStatus": map[string]any{"status": "COMPLETE"}, "imageScanFindings": map[string]any{
				"findingSeverityCounts": map[string]int{"CRITICAL": 1}, "enhancedFindings": []map[string]any{{
					"severity": "CRITICAL",
					"packageVulnerabilityDetails": map[string]any{"vulnerabilityId": "CVE-2024-3094", "sourceUrl": "https://nvd.nist.gov/vuln/detail/CVE-2024-3094",
						"vulnerablePackages": []map[string]any{{"name": "xz-utils", "version": "5.6.0", "fixedInVersion": "5.6.1"}}},
				}}}})
		default:
			http.Error(w, "unexpected "+target, http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	r, err := New(context.Background(), host,
		awsconfig.WithBaseEndpoint(srv.URL),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")))
	if err != nil {
		t.Fatal(err)
	}
	repos, err := r.Repos(context.Background())
	if err != nil || len(repos) != 1 || repos[0] != "payments/api" {
		t.Fatalf("repos = %v, %v", repos, err)
	}
	images, err := r.Images(context.Background(), "payments/api")
	if err != nil || len(images) != 1 || images[0].PushedAt.Unix() != 1764547200 {
		t.Fatalf("images = %+v, %v", images, err)
	}
	if sc := images[0].Scan; sc == nil || sc.Status != model.ScanComplete || sc.Critical != 2 || sc.Other != 1 || sc.CompletedAt.Unix() != 1764550800 {
		t.Fatalf("scan = %+v", images[0].Scan)
	}
	sc, err := r.Scan(context.Background(), "payments/api", "sha256:1")
	if err != nil || sc.Status != model.ScanComplete || sc.Critical != 1 || len(sc.Findings) != 1 || sc.Findings[0] != (model.Finding{ID: "CVE-2024-3094", Package: "xz-utils", Version: "5.6.0", Severity: "critical", FixedIn: "5.6.1", URI: "https://nvd.nist.gov/vuln/detail/CVE-2024-3094"}) {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
	got, err := r.Manifests(context.Background(), "payments/api", []string{digestOf(stubManifest)})
	if err != nil || string(got[digestOf(stubManifest)]) != stubManifest {
		t.Fatalf("manifests = %v, %v", got, err)
	}
	if r.Calls() != 4 {
		t.Fatalf("calls = %d, want 4", r.Calls())
	}
}

func TestScanOf(t *testing.T) {
	t.Parallel()
	done := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		in   types.ImageDetail
		want *model.Scan
	}{
		{"never scanned", types.ImageDetail{}, nil},
		{"basic scan", types.ImageDetail{
			ImageScanStatus: &types.ImageScanStatus{Status: types.ScanStatusComplete},
			ImageScanFindingsSummary: &types.ImageScanFindingsSummary{ImageScanCompletedAt: aws.Time(done),
				FindingSeverityCounts: map[string]int32{"CRITICAL": 1, "HIGH": 2, "MEDIUM": 3, "LOW": 4, "INFORMATIONAL": 5, "UNDEFINED": 1}},
		}, &model.Scan{Source: "ecr", Status: "complete", CompletedAt: done, Critical: 1, High: 2, Medium: 3, Low: 4, Other: 6}},
		{"failed", types.ImageDetail{ImageScanStatus: &types.ImageScanStatus{Status: types.ScanStatusUnsupportedImage}},
			&model.Scan{Source: "ecr", Status: "unsupported_image"}},
		{"summary without status", types.ImageDetail{ImageScanFindingsSummary: &types.ImageScanFindingsSummary{ImageScanCompletedAt: aws.Time(done)}},
			&model.Scan{Source: "ecr", Status: "complete", CompletedAt: done}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scanOf(tc.in)
			if (got == nil) != (tc.want == nil) || got != nil && !reflect.DeepEqual(*got, *tc.want) {
				t.Fatalf("scanOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestScan(t *testing.T) {
	t.Parallel()
	done := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	api := &fakeAPI{findings: []*types.ImageScanFindings{
		{ImageScanCompletedAt: aws.Time(done), FindingSeverityCounts: map[string]int32{"HIGH": 1, "MEDIUM": 1, "LOW": 1}, Findings: []types.ImageScanFinding{{Name: aws.String("CVE-2023-1"), Severity: types.FindingSeverityHigh, Uri: aws.String("https://x/1"),
			Attributes: []types.Attribute{{Key: aws.String("package_name"), Value: aws.String("openssl")}, {Key: aws.String("package_version"), Value: aws.String("3.0.1")}}}}},
		nil,
		{EnhancedFindings: []types.EnhancedImageScanFinding{
			{Severity: aws.String("MEDIUM"), PackageVulnerabilityDetails: &types.PackageVulnerabilityDetails{VulnerabilityId: aws.String("CVE-2023-2"),
				VulnerablePackages: []types.VulnerablePackage{{Name: aws.String("zlib"), Version: aws.String("1.2"), FixedInVersion: aws.String("1.3")}, {Name: aws.String("zlib-dev"), Version: aws.String("1.2")}}}},
			{Severity: aws.String("LOW"), PackageVulnerabilityDetails: &types.PackageVulnerabilityDetails{VulnerabilityId: aws.String("CVE-2023-3")}},
			{Severity: aws.String("LOW")},
		}},
	}}
	r := NewWithAPI(context.Background(), host, "123456789012", api)
	sc, err := r.Scan(context.Background(), "a", "sha256:1")
	if err != nil {
		t.Fatal(err)
	}
	got := sc.Findings
	if sc.Status != model.ScanComplete || !sc.CompletedAt.Equal(done) || sc.High != 1 || sc.Low != 1 || !sc.HasFindings || sc.Digest != "sha256:1" {
		t.Fatalf("scan = %+v", sc)
	}
	want := []model.Finding{
		{ID: "CVE-2023-1", Package: "openssl", Version: "3.0.1", Severity: "high", URI: "https://x/1"},
		{ID: "CVE-2023-2", Package: "zlib", Version: "1.2", Severity: "medium", FixedIn: "1.3"},
		{ID: "CVE-2023-2", Package: "zlib-dev", Version: "1.2", Severity: "medium"},
		{ID: "CVE-2023-3", Severity: "low"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v", got)
	}
	if r.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", r.Calls())
	}
	api.err = &types.ScanNotFoundException{Message: aws.String("no scan")}
	if sc, err := r.Scan(context.Background(), "a", "sha256:1"); err != nil || sc.Status != model.ScanNone || !sc.HasFindings {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
	api.err = errors.New("AccessDenied")
	if _, err := r.Scan(context.Background(), "a", "sha256:1"); err == nil || !strings.Contains(err.Error(), "describe scan findings of a@sha256:1") {
		t.Fatalf("err = %v", err)
	}
}

// digestOf returns the sha256 digest of s.
func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestManifests(t *testing.T) {
	t.Parallel()
	good := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	api := &fakeAPI{manifests: map[string]string{
		digestOf(good): good,
		// ECR returning bytes that do not match the digest asked for, as
		// a converted manifest would, is left out.
		"sha256:" + strings.Repeat("0", 64): good,
	}}
	r := NewWithAPI(context.Background(), host, "123456789012", api)
	missing := "sha256:" + strings.Repeat("1", 64)
	got, err := r.Manifests(context.Background(), "payments/api", []string{digestOf(good), "sha256:" + strings.Repeat("0", 64), missing})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[digestOf(good)]) != good {
		t.Fatalf("manifests = %v", got)
	}
	if r.Calls() != 1 || len(api.asked) != 1 || len(api.asked[0]) != 3 {
		t.Fatalf("calls = %d, asked = %v", r.Calls(), api.asked)
	}
	api.err = errors.New("AccessDenied")
	if _, err := r.Manifests(context.Background(), "payments/api", []string{missing}); err == nil || !strings.Contains(err.Error(), "batch get images in payments/api") {
		t.Fatalf("err = %v", err)
	}
}
