package improvekandev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/kandev/kandev/internal/auth/authn"
	"github.com/kandev/kandev/internal/common/logger"
	"github.com/kandev/kandev/internal/db"
	"github.com/kandev/kandev/internal/events/bus"
	"github.com/kandev/kandev/internal/github"
	"github.com/kandev/kandev/internal/system/logbundle"
	taskmodels "github.com/kandev/kandev/internal/task/models"
	"github.com/kandev/kandev/internal/task/repository"
	tasksqlite "github.com/kandev/kandev/internal/task/repository/sqlite"
	taskservice "github.com/kandev/kandev/internal/task/service"
)

// fakeGitHubInfo is a configurable GitHubInfo for unit tests. Each method
// returns the value of the corresponding field; if the err counterpart is
// non-nil, the value is ignored and the error is returned instead.
type fakeGitHubInfo struct {
	login             string
	loginErr          error
	providerRepoID    string
	providerRepoIDErr error
	hasWrite          bool
	hasWriteErr       error
	hasFork           bool
	hasForkErr        error
	calledHasFork     bool
}

type fakeManagedGitHub struct {
	policy    github.TaskGitCredentialPolicy
	policyErr error
	result    github.ContributionForkResolution
	resultErr error
	probed    bool
}

func (f *fakeManagedGitHub) DescribeTaskGitCredentialPolicy(context.Context, string) (github.TaskGitCredentialPolicy, error) {
	return f.policy, f.policyErr
}

func (f *fakeManagedGitHub) ProbeContributionForkCapabilityForWorkspace(context.Context, string, string, string) (github.ContributionForkResolution, error) {
	f.probed = true
	return f.result, f.resultErr
}

func TestResolveGitHubAccessForWorkspaceUsesManagedForkCapability(t *testing.T) {
	managed := &fakeManagedGitHub{
		policy: github.TaskGitCredentialPolicy{Mode: github.TaskGitCredentialsModeManaged},
		result: github.ContributionForkResolution{
			Status: github.ContributionForkStatusCreatable, ActorLogin: "automation",
			Repository: &github.GitHubRepository{ID: 100},
		},
	}
	handler := newTestHandler(&fakeGitHubInfo{login: "ambient", hasWrite: true})
	handler.SetManagedGitHubForkProber(managed)

	access := handler.resolveGitHubAccessForWorkspace(context.Background(), "workspace-1")
	if access.forkStatus != ForkStatusCreatable || access.login != "automation" || access.providerRepoID != "100" || !managed.probed {
		t.Fatalf("managed access = %+v, probed=%v", access, managed.probed)
	}
}

func TestResolveGitHubAccessForWorkspaceBlocksManagedErrorsWithoutAmbientFallback(t *testing.T) {
	managed := &fakeManagedGitHub{
		policy:    github.TaskGitCredentialPolicy{Mode: github.TaskGitCredentialsModeManaged},
		resultErr: github.ErrContributionForkAppUnsupported,
	}
	handler := newTestHandler(&fakeGitHubInfo{login: "ambient", hasWrite: true})
	handler.SetManagedGitHubForkProber(managed)

	access := handler.resolveGitHubAccessForWorkspace(context.Background(), "workspace-1")
	if access.forkStatus != ForkStatusBlockedManaged || access.login != "" || access.hasWrite {
		t.Fatalf("managed error access = %+v", access)
	}
}

func TestResolveGitHubAccessFallsBackToCanonicalProviderID(t *testing.T) {
	handler := newTestHandler(&fakeGitHubInfo{
		login: "alice", providerRepoID: "100", hasWrite: true,
	})

	access := handler.resolveGitHubAccess(context.Background())
	if access.providerRepoID != "100" {
		t.Fatalf("providerRepoID = %q, want 100", access.providerRepoID)
	}
}

func (f *fakeGitHubInfo) GetAuthenticatedLogin(_ context.Context) (string, error) {
	return f.login, f.loginErr
}

func (f *fakeGitHubInfo) GetRepositoryID(_ context.Context, _, _ string) (string, error) {
	return f.providerRepoID, f.providerRepoIDErr
}

func (f *fakeGitHubInfo) HasRepoWriteAccess(_ context.Context, _, _ string) (bool, error) {
	return f.hasWrite, f.hasWriteErr
}

func (f *fakeGitHubInfo) UserHasFork(_ context.Context, _, _ string) (bool, error) {
	f.calledHasFork = true
	return f.hasFork, f.hasForkErr
}

func newTestHandler(gh GitHubInfo) *Handler {
	return &Handler{gh: gh, log: logger.Default()}
}

type fakeDiagnosticBundles struct {
	path  string
	owner string
	id    string
}

func (f *fakeDiagnosticBundles) OpenArchive(owner, id string) (*os.File, logbundle.JobView, error) {
	f.owner, f.id = owner, id
	file, err := os.Open(f.path)
	return file, logbundle.JobView{ID: id, Status: logbundle.StatusReady, Sources: []string{"backend", "frontend"}}, err
}

func TestLeaseBundleChecksMarkerOwnerAndCopiesArchive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir, err := createBundleDir("user-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("zip bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundles := &fakeDiagnosticBundles{path: source}
	handler := &Handler{log: logger.Default(), logBundles: bundles}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authn.SetOnGin(c, authn.Identity{UserID: "user-1"})
		c.Next()
	})
	router.POST("/lease", handler.httpLeaseBundle)

	body, _ := json.Marshal(leaseBundleRequest{BundleDir: dir, BundleID: "bundle-1"})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/lease", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if bundles.owner != "user-1" || bundles.id != "bundle-1" {
		t.Fatalf("OpenArchive identity = %q/%q", bundles.owner, bundles.id)
	}
	data, err := os.ReadFile(filepath.Join(dir, diagnosticFileName))
	if err != nil || string(data) != "zip bytes" {
		t.Fatalf("leased archive = %q, err=%v", data, err)
	}

	otherDir, err := createBundleDir("user-2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(otherDir) })
	body, _ = json.Marshal(leaseBundleRequest{BundleDir: otherDir, BundleID: "bundle-1"})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/lease", bytes.NewReader(body)))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("other owner status = %d, want 403", recorder.Code)
	}
}

func TestResolveGitHubAccess_Writable(t *testing.T) {
	gh := &fakeGitHubInfo{login: "alice", hasWrite: true}
	access := newTestHandler(gh).resolveGitHubAccess(context.Background())
	if access.login != "alice" || !access.hasWrite {
		t.Fatalf("login/write mismatch: %+v", access)
	}
	if access.forkStatus != ForkStatusWritable {
		t.Errorf("fork status = %q, want %q", access.forkStatus, ForkStatusWritable)
	}
	if gh.calledHasFork {
		t.Errorf("writable users should short-circuit before the fork check")
	}
}

func TestResolveGitHubAccess_ForkAlreadyExists(t *testing.T) {
	gh := &fakeGitHubInfo{login: "bob_corp", hasWrite: false, hasFork: true}
	access := newTestHandler(gh).resolveGitHubAccess(context.Background())
	if access.forkStatus != ForkStatusReady {
		t.Errorf("fork status = %q, want %q", access.forkStatus, ForkStatusReady)
	}
	if access.forkReasonCode != "" {
		t.Errorf("ready status must not include a fork reason even for EMU-shaped logins: %q", access.forkReasonCode)
	}
}

func TestResolveGitHubAccess_BlockedEMU(t *testing.T) {
	gh := &fakeGitHubInfo{login: "eve_corp", hasWrite: false, hasFork: false}
	access := newTestHandler(gh).resolveGitHubAccess(context.Background())
	if access.forkStatus != ForkStatusBlockedEMU {
		t.Errorf("fork status = %q, want %q", access.forkStatus, ForkStatusBlockedEMU)
	}
	if access.forkReasonCode == "" {
		t.Errorf("blocked_emu must include a fork reason code for the dialog")
	}
}

func TestResolveGitHubAccess_UnknownOnLoginError(t *testing.T) {
	gh := &fakeGitHubInfo{loginErr: errors.New("auth failed")}
	access := newTestHandler(gh).resolveGitHubAccess(context.Background())
	if access.login != "" || access.hasWrite {
		t.Errorf("login error must yield empty access: %+v", access)
	}
	if access.forkStatus != ForkStatusUnknown {
		t.Errorf("fork status = %q, want %q", access.forkStatus, ForkStatusUnknown)
	}
}

func TestResolveGitHubAccess_UnknownOnForkLookupError(t *testing.T) {
	gh := &fakeGitHubInfo{login: "carol_corp", hasWrite: false, hasForkErr: errors.New("network")}
	access := newTestHandler(gh).resolveGitHubAccess(context.Background())
	if access.forkStatus != ForkStatusUnknown {
		t.Errorf("fork status = %q, want %q", access.forkStatus, ForkStatusUnknown)
	}
	if access.forkReasonCode != "" {
		t.Errorf("fork lookup failures must not produce an EMU reason even for underscore logins")
	}
}

func TestResolveGitHubAccess_NoForkNotEMU(t *testing.T) {
	gh := &fakeGitHubInfo{login: "frank", hasWrite: false, hasFork: false}
	access := newTestHandler(gh).resolveGitHubAccess(context.Background())
	if access.forkStatus != ForkStatusUnknown {
		t.Errorf("fork status = %q, want %q", access.forkStatus, ForkStatusUnknown)
	}
	if access.forkReasonCode != "" {
		t.Errorf("non-EMU users should not get a fork reason code")
	}
}

func TestIsEMULogin(t *testing.T) {
	cases := []struct {
		login string
		want  bool
	}{
		{"alice", false},
		{"alice-bob", false},
		{"", false},
		{"alice_corp", true},
		{"Carlos-Florencio-ii3_nbcuni", true},
	}
	for _, tc := range cases {
		if got := isEMULogin(tc.login); got != tc.want {
			t.Errorf("isEMULogin(%q) = %v, want %v", tc.login, got, tc.want)
		}
	}
}

func TestFindKandevRepoByLocalRemote(t *testing.T) {
	resolver := func(path string) (string, string, string) {
		switch path {
		case "/home/u/kandev":
			return "github", "kdlbs", "kandev"
		case "/home/u/fork":
			return "github", "alice", "kandev"
		case "/home/u/other":
			return "github", "kdlbs", "other"
		}
		return "", "", ""
	}

	cases := []struct {
		name  string
		repos []*taskmodels.Repository
		want  string // matched repo ID, or "" for no match
	}{
		{name: "empty list", repos: nil, want: ""},
		{
			name: "skips entries without local path",
			repos: []*taskmodels.Repository{
				{ID: "no-path", LocalPath: ""},
			},
			want: "",
		},
		{
			name: "matches remote owner/name with no provider info",
			repos: []*taskmodels.Repository{
				{ID: "match", LocalPath: "/home/u/kandev"},
			},
			want: "match",
		},
		{
			name: "skips fork (different owner)",
			repos: []*taskmodels.Repository{
				{ID: "fork", LocalPath: "/home/u/fork"},
			},
			want: "",
		},
		{
			name: "skips repo already wired to a different provider",
			repos: []*taskmodels.Repository{
				{ID: "wired", LocalPath: "/home/u/kandev", Provider: "github", ProviderOwner: "someone", ProviderName: "kandev"},
			},
			want: "",
		},
		{
			name: "returns first match when multiple candidates",
			repos: []*taskmodels.Repository{
				{ID: "first", LocalPath: "/home/u/kandev"},
				{ID: "second", LocalPath: "/home/u/kandev"},
			},
			want: "first",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findKandevRepoByLocalRemote(tc.repos, resolver)
			if tc.want == "" {
				if got != nil {
					t.Errorf("expected no match, got %q", got.ID)
				}
				return
			}
			if got == nil || got.ID != tc.want {
				t.Errorf("got = %v, want id = %q", got, tc.want)
			}
		})
	}
}

func TestFindKandevRepoByLocalRemote_NilResolver(t *testing.T) {
	repos := []*taskmodels.Repository{{ID: "x", LocalPath: "/p"}}
	if got := findKandevRepoByLocalRemote(repos, nil); got != nil {
		t.Errorf("nil resolver must return nil, got %v", got)
	}
}

// TestEnsureKandevProviderRepoID_NeverPersistsUnpairedRepoID guards the
// kdlbs/kandev bootstrap regression where backfilling provider_repo_id onto
// a repository row with no provider_scope left the row unusable at the next
// task session's workspace setup ("provider scope and repository ID must be
// supplied together" from repoclone.Cloner.WorkspaceProviderRepositoryPath).
// The built-in GitHub repository flow this handler drives never resolves a
// provider connection scope, so ensureKandevProviderRepoID must never write
// provider_repo_id on its own.
func TestEnsureKandevProviderRepoID_NeverPersistsUnpairedRepoID(t *testing.T) {
	h := newTestHandler(nil)
	repo := &taskmodels.Repository{ID: "repo-1", Provider: "github", ProviderOwner: "kdlbs", ProviderName: "kandev"}

	if err := h.ensureKandevProviderRepoID(context.Background(), repo, "1131388506"); err != nil {
		t.Fatalf("ensureKandevProviderRepoID() error = %v, want nil", err)
	}
	if repo.ProviderRepoID != "" {
		t.Fatalf("ProviderRepoID = %q, want unchanged empty (would violate the provider_scope/provider_repo_id pair invariant)", repo.ProviderRepoID)
	}
}

func TestEnsureKandevProviderRepoID_DetectsIdentityDriftOnAlreadyPairedRow(t *testing.T) {
	h := newTestHandler(nil)
	repo := &taskmodels.Repository{
		ID: "repo-1", Provider: "github", ProviderOwner: "kdlbs", ProviderName: "kandev",
		ProviderScope: "kdlbs", ProviderRepoID: "1131388506",
	}

	if err := h.ensureKandevProviderRepoID(context.Background(), repo, "1131388506"); err != nil {
		t.Fatalf("matching ID: ensureKandevProviderRepoID() error = %v, want nil", err)
	}

	err := h.ensureKandevProviderRepoID(context.Background(), repo, "999999999")
	if err == nil {
		t.Fatal("mismatched ID: ensureKandevProviderRepoID() error = nil, want identity-changed error")
	}
	if repo.ProviderRepoID != "1131388506" {
		t.Fatalf("ProviderRepoID = %q, want unchanged %q after a rejected identity change", repo.ProviderRepoID, "1131388506")
	}
}

// newEnsureProviderRepoIDTestService builds a real, minimal
// *taskservice.Service backed by a temp SQLite DB. ensureKandevProviderRepoID
// heals rows through h.taskSvc.UpdateRepository, so exercising it end to end
// needs a real service rather than a fake.
func newEnsureProviderRepoIDTestService(t *testing.T) (*taskservice.Service, *tasksqlite.Repository) {
	t.Helper()
	dbConn, err := db.OpenSQLite(filepath.Join(t.TempDir(), "ensure-provider-repo-id.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	database := sqlx.NewDb(dbConn, "sqlite3")
	t.Cleanup(func() { _ = database.Close() })
	repo, cleanup, err := repository.Provide(database, database, nil)
	if err != nil {
		t.Fatalf("task repository: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	svc := taskservice.NewService(taskservice.Repos{
		Workspaces:   repo,
		Tasks:        repo,
		TaskRepos:    repo,
		Workflows:    repo,
		Messages:     repo,
		Turns:        repo,
		Sessions:     repo,
		GitSnapshots: repo,
		RepoEntities: repo,
	}, bus.NewMemoryEventBus(logger.Default()), logger.Default(), taskservice.RepositoryDiscoveryConfig{})
	return svc, repo
}

// TestEnsureKandevProviderRepoID_HealsExistingUnpairedRow reproduces the
// live production row (kdlbs/kandev, Improve Kandev workspace) a previous
// version of this bootstrap left with provider_repo_id set and
// provider_scope empty, which made every task session in that workspace
// fail workspace setup with "provider scope and repository ID must be
// supplied together". ensureKandevProviderRepoID must clear the unpaired ID
// so the row becomes usable again via the legacy owner/name clone path.
func TestEnsureKandevProviderRepoID_HealsExistingUnpairedRow(t *testing.T) {
	taskSvc, rawRepo := newEnsureProviderRepoIDTestService(t)
	h := &Handler{taskSvc: taskSvc, log: logger.Default()}
	ctx := context.Background()

	if err := rawRepo.CreateWorkspace(ctx, &taskmodels.Workspace{ID: "ws-improve", Name: "Improve Kandev"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	// Bypass service validation to plant the exact corrupted legacy state:
	// provider_repo_id set, provider_scope empty. The (now-guarded) service
	// layer would reject writing this pair going forward.
	corrupted := &taskmodels.Repository{
		ID: "repo-kandev", WorkspaceID: "ws-improve", Name: "kdlbs/kandev",
		Provider: "github", ProviderHost: "https://github.com",
		ProviderOwner: "kdlbs", ProviderName: "kandev", ProviderRepoID: "1131388506",
	}
	if err := rawRepo.CreateRepository(ctx, corrupted); err != nil {
		t.Fatalf("plant corrupted repository: %v", err)
	}

	if err := h.ensureKandevProviderRepoID(ctx, corrupted, "1131388506"); err != nil {
		t.Fatalf("ensureKandevProviderRepoID() error = %v, want nil", err)
	}
	if corrupted.ProviderRepoID != "" {
		t.Fatalf("in-memory ProviderRepoID = %q, want healed to empty", corrupted.ProviderRepoID)
	}
	stored, err := taskSvc.GetRepository(ctx, "repo-kandev")
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if stored.ProviderRepoID != "" {
		t.Fatalf("persisted ProviderRepoID = %q, want healed to empty", stored.ProviderRepoID)
	}
}

func TestIsGHNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("network unreachable"), false},
		{"http 404", errors.New("gh api: exit status 1: HTTP 404: Not Found"), true},
		{"http 403", errors.New("gh api: HTTP 403: Forbidden"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGHNotFound(tc.err); got != tc.want {
				t.Errorf("isGHNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
