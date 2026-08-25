package foundry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// recordingStager records what was staged. It implements the one method under
// test explicitly rather than embedding foundryClient, so a call the test does
// not expect fails to compile instead of panicking at runtime.
type recordingStager struct {
	foundryClient
	calls []stagedCall
	err   error
}

type stagedCall struct {
	URI  string
	Data []byte
}

func (r *recordingStager) StageObject(_ context.Context, uri string, data []byte) error {
	r.calls = append(r.calls, stagedCall{URI: uri, Data: data})
	return r.err
}

func TestStagePackSkipsAnInlinePack(t *testing.T) {
	stager := &recordingStager{}
	in := &planContext{
		Cfg:      &Config{StagingContainer: "https://acct.blob.core.windows.net/packs"},
		Delivery: PackDelivery{Inline: true, SizeBytes: 10},
	}

	uri, err := stagePack(context.Background(), stager, in, `{"id":"p"}`)
	if err != nil {
		t.Fatalf("stagePack: %v", err)
	}
	if uri != "" {
		t.Errorf("uri = %q, want empty for an inline pack", uri)
	}
	if len(stager.calls) != 0 {
		t.Errorf("staged %d objects, want none", len(stager.calls))
	}
}

// The whole point of staging: a pack over the limit is uploaded and the agent
// is pointed at it. Before this existed the URI was never assigned, so every
// oversized pack failed the deploy.
func TestStagePackUploadsAnOversizedPack(t *testing.T) {
	stager := &recordingStager{}
	in := &planContext{
		Cfg:      &Config{StagingContainer: "https://acct.blob.core.windows.net/packs"},
		Delivery: PackDelivery{Inline: false, SizeBytes: 40000},
		PackHash: "abc123",
	}

	uri, err := stagePack(context.Background(), stager, in, `{"id":"big"}`)
	if err != nil {
		t.Fatalf("stagePack: %v", err)
	}

	want := "https://acct.blob.core.windows.net/packs/abc123.json"
	if uri != want {
		t.Errorf("uri = %q, want %q", uri, want)
	}
	if len(stager.calls) != 1 {
		t.Fatalf("staged %d objects, want 1", len(stager.calls))
	}
	if got := string(stager.calls[0].Data); got != `{"id":"big"}` {
		t.Errorf("staged body = %q", got)
	}
}

// Blob names are content-addressed, so re-staging the same hash would rewrite
// identical bytes. Skipping it keeps a no-op apply from touching storage.
func TestStagePackSkipsAnUnchangedPack(t *testing.T) {
	stager := &recordingStager{}
	in := &planContext{
		Cfg:      &Config{StagingContainer: "https://acct.blob.core.windows.net/packs"},
		Delivery: PackDelivery{Inline: false, SizeBytes: 40000},
		PackHash: "abc123",
		Prior:    &State{StagedPackURI: "https://acct.blob.core.windows.net/packs/abc123.json"},
	}

	uri, err := stagePack(context.Background(), stager, in, `{"id":"big"}`)
	if err != nil {
		t.Fatalf("stagePack: %v", err)
	}
	if uri == "" {
		t.Error("uri is empty; the agent would lose its pack")
	}
	if len(stager.calls) != 0 {
		t.Errorf("staged %d objects, want none for an unchanged hash", len(stager.calls))
	}
}

// A changed pack must re-upload even though prior state holds a staged URI.
func TestStagePackUploadsWhenTheHashChanges(t *testing.T) {
	stager := &recordingStager{}
	in := &planContext{
		Cfg:      &Config{StagingContainer: "https://acct.blob.core.windows.net/packs"},
		Delivery: PackDelivery{Inline: false, SizeBytes: 40000},
		PackHash: "def456",
		Prior:    &State{StagedPackURI: "https://acct.blob.core.windows.net/packs/abc123.json"},
	}

	if _, err := stagePack(context.Background(), stager, in, `{"id":"new"}`); err != nil {
		t.Fatalf("stagePack: %v", err)
	}
	if len(stager.calls) != 1 {
		t.Fatalf("staged %d objects, want 1", len(stager.calls))
	}
}

func TestStagePackReportsAnUploadFailure(t *testing.T) {
	stager := &recordingStager{err: errors.New("403 forbidden")}
	in := &planContext{
		Cfg:      &Config{StagingContainer: "https://acct.blob.core.windows.net/packs"},
		Delivery: PackDelivery{Inline: false, SizeBytes: 40000},
		PackHash: "abc123",
	}

	_, err := stagePack(context.Background(), stager, in, `{"id":"big"}`)
	if err == nil {
		t.Fatal("stagePack succeeded despite an upload failure")
	}
	if !strings.Contains(err.Error(), "403 forbidden") {
		t.Errorf("error = %v, want the upload cause", err)
	}
}

// An oversized pack with nowhere to stage it has to fail at plan time. The
// earlier behaviour listed a pack_object in the plan and failed at apply.
func TestValidatePackDeliveryRejectsAnUnstageablePack(t *testing.T) {
	errs := validatePackDelivery(
		PackDelivery{Inline: false, SizeBytes: 40000}, &Config{})
	if len(errs) == 0 {
		t.Fatal("validatePackDelivery accepted a pack with no staging_container")
	}
	if !strings.Contains(errs[0], "staging_container") {
		t.Errorf("error = %q, want it to name staging_container", errs[0])
	}
}

func TestValidatePackDeliveryAcceptsAStageablePack(t *testing.T) {
	errs := validatePackDelivery(
		PackDelivery{Inline: false, SizeBytes: 40000},
		&Config{StagingContainer: "https://acct.blob.core.windows.net/packs"})
	if len(errs) != 0 {
		t.Errorf("validatePackDelivery = %v, want none", errs)
	}
}

func TestValidatePackDeliveryAcceptsAnInlinePack(t *testing.T) {
	if errs := validatePackDelivery(
		PackDelivery{Inline: true, SizeBytes: 10}, &Config{}); len(errs) != 0 {
		t.Errorf("validatePackDelivery = %v, want none", errs)
	}
}

func TestParseBlobURI(t *testing.T) {
	got, err := parseBlobURI("https://acct.blob.core.windows.net/packs/abc123.json")
	if err != nil {
		t.Fatalf("parseBlobURI: %v", err)
	}
	if got.Service != "https://acct.blob.core.windows.net" {
		t.Errorf("Service = %q", got.Service)
	}
	if got.Container != "packs" {
		t.Errorf("Container = %q", got.Container)
	}
	if got.Blob != "abc123.json" {
		t.Errorf("Blob = %q", got.Blob)
	}
}

func TestParseBlobURIRejectsAContainerlessURL(t *testing.T) {
	if _, err := parseBlobURI("https://acct.blob.core.windows.net/"); err == nil {
		t.Fatal("parseBlobURI accepted a URL naming no container")
	}
}

// blobTestClient wires a restClient's Blob path to an httptest server.
func blobTestClient(t *testing.T, handler http.HandlerFunc) (*restClient, string) {
	t.Helper()
	srv := newTLSServer(t, handler)
	return &restClient{cred: fakeCredential{}, blobTransport: srv.Client()}, srv.URL
}

// The upload is the whole point of staging, so it is exercised rather than
// assumed: a blob URI has to become a PUT of the pack bytes to the right path.
func TestStageObjectUploadsToTheBlobPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	client, base := blobTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusCreated)
	})

	err := client.StageObject(
		context.Background(), base+"/packs/abc123.json", []byte(`{"id":"p"}`))
	if err != nil {
		t.Fatalf("StageObject: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/packs/abc123.json" {
		t.Errorf("path = %q, want /packs/abc123.json", gotPath)
	}
	if string(gotBody) != `{"id":"p"}` {
		t.Errorf("body = %q", gotBody)
	}
}

// A refused upload has to surface. Staging that fails quietly would leave the
// agent pointed at a blob that does not exist.
func TestStageObjectReportsAFailedUpload(t *testing.T) {
	client, base := blobTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	err := client.StageObject(
		context.Background(), base+"/packs/abc123.json", []byte(`{}`))
	if err == nil {
		t.Fatal("StageObject succeeded on a 403")
	}
}

func TestStageObjectWithoutACredential(t *testing.T) {
	c := &restClient{}
	if err := c.StageObject(
		context.Background(), "https://a.blob.core.windows.net/p/x.json", nil); err == nil {
		t.Fatal("StageObject succeeded with no credential")
	}
}

func TestStageObjectRejectsAMalformedURI(t *testing.T) {
	c := &restClient{cred: fakeCredential{}}
	if err := c.StageObject(context.Background(), "https://a.blob.core.windows.net/", nil); err == nil {
		t.Fatal("StageObject accepted a URI naming no blob")
	}
}

// stagePack refuses rather than silently inlining when it has nowhere to stage.
func TestStagePackWithoutAContainer(t *testing.T) {
	in := &planContext{
		Cfg:      &Config{},
		Delivery: PackDelivery{Inline: false, SizeBytes: 40000},
	}
	if _, err := stagePack(context.Background(), &recordingStager{}, in, "{}"); err == nil {
		t.Fatal("stagePack succeeded with no staging_container")
	}
}

func TestEffectiveInlineLimitFallsBackToTheDefault(t *testing.T) {
	if got := effectiveInlineLimit(&Config{}); got != DefaultPackInlineLimitBytes {
		t.Errorf("effectiveInlineLimit = %d, want %d", got, DefaultPackInlineLimitBytes)
	}
	if got := effectiveInlineLimit(&Config{PackInlineLimitBytes: 99}); got != 99 {
		t.Errorf("effectiveInlineLimit = %d, want 99", got)
	}
}
