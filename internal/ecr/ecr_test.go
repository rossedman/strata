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
	if _, err := New(context.Background(), "reg.io", Credentials{}); err == nil {
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
			}}})
		default:
			http.Error(w, "unexpected "+target, http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	r, err := New(context.Background(), host, Credentials{},
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
	got, err := r.Manifests(context.Background(), "payments/api", []string{digestOf(stubManifest)})
	if err != nil || string(got[digestOf(stubManifest)]) != stubManifest {
		t.Fatalf("manifests = %v, %v", got, err)
	}
	if r.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", r.Calls())
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

// TestNewWithCredentials assumes a role, with an external ID, before
// calling ECR with the role's credentials, against stub STS and ECR
// endpoints.
func TestNewWithCredentials(t *testing.T) {
	t.Parallel()
	var assumed, signed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Amz-Target") == "" {
			// STS speaks the query protocol.
			_ = req.ParseForm()
			assumed = req.Form.Get("Action") + " " + req.Form.Get("RoleArn") + " " + req.Form.Get("ExternalId") + " " + req.Form.Get("RoleSessionName")
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>
				<AccessKeyId>ASIAROLE</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>tok</SessionToken>
				<Expiration>2099-01-01T00:00:00Z</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::111111111111:assumed-role/r/strata</Arn>
				<AssumedRoleId>id</AssumedRoleId></AssumedRoleUser></AssumeRoleResult></AssumeRoleResponse>`))
			return
		}
		signed = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"repositoryName": "base/node"}}})
	}))
	t.Cleanup(srv.Close)
	r, err := New(context.Background(), host, Credentials{RoleARN: "arn:aws:iam::111111111111:role/strata-read", ExternalID: "ext"},
		awsconfig.WithBaseEndpoint(srv.URL),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKIDBASE", "SECRET", "")))
	if err != nil {
		t.Fatal(err)
	}
	repos, err := r.Repos(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v, %v", repos, err)
	}
	if assumed != "AssumeRole arn:aws:iam::111111111111:role/strata-read ext strata" {
		t.Fatalf("assume role request = %q", assumed)
	}
	if !strings.Contains(signed, "Credential=ASIAROLE/") {
		t.Fatalf("ECR call not signed with the role: %q", signed)
	}
	// A profile that does not exist fails up front.
	if _, err := New(context.Background(), host, Credentials{Profile: "strata-test-missing-profile"}); err == nil {
		t.Fatal("a missing profile was accepted")
	}
}
