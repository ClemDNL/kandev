package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/kandev/kandev/internal/task/models"
)

func TestApplyRepositoryUpdatesRejectsInvalidProviderScopeWithoutClearingIdentity(t *testing.T) {
	repository := &models.Repository{ProviderScope: "existing-scope"}
	invalid := strings.Repeat("x", maxProviderScopeBytes+1)

	err := applyRepositoryUpdates(repository, &UpdateRepositoryRequest{ProviderScope: &invalid})

	if !errors.Is(err, ErrInvalidRepositorySettings) {
		t.Fatalf("applyRepositoryUpdates error = %v, want ErrInvalidRepositorySettings", err)
	}
	if repository.ProviderScope != "existing-scope" {
		t.Fatalf("invalid scope changed repository identity to %q", repository.ProviderScope)
	}
}

// TestApplyRepositoryUpdatesAcceptsProviderRepoIDWithoutScope guards
// repoclone.Cloner.WorkspaceProviderRepositoryPath's actual invariant: only
// a non-empty provider_scope requires a paired provider_repo_id. A bare
// provider_repo_id with no scope is the normal shape for every built-in
// provider (GitHub, GitLab, Azure DevOps) — none of which resolve a
// provider connection scope — and must stay accepted.
func TestApplyRepositoryUpdatesAcceptsProviderRepoIDWithoutScope(t *testing.T) {
	repository := &models.Repository{}
	repoID := "1131388506"

	if err := applyRepositoryUpdates(repository, &UpdateRepositoryRequest{ProviderRepoID: &repoID}); err != nil {
		t.Fatalf("applyRepositoryUpdates error = %v, want nil", err)
	}
	if repository.ProviderRepoID != repoID {
		t.Fatalf("ProviderRepoID = %q, want %q", repository.ProviderRepoID, repoID)
	}
}

// TestApplyRepositoryUpdatesRejectsProviderScopeWithoutRepoID guards the
// direction that actually breaks WorkspaceProviderRepositoryPath: a scope
// with no repository ID can't build a unique scoped clone path.
func TestApplyRepositoryUpdatesRejectsProviderScopeWithoutRepoID(t *testing.T) {
	repository := &models.Repository{}
	scope := "forge-instance-a"

	err := applyRepositoryUpdates(repository, &UpdateRepositoryRequest{ProviderScope: &scope})

	if !errors.Is(err, ErrInvalidRepositorySettings) {
		t.Fatalf("applyRepositoryUpdates error = %v, want ErrInvalidRepositorySettings", err)
	}
}

// TestApplyRepositoryUpdatesRejectsWhitespaceOnlyRepoIDWithScope guards
// against a whitespace-only provider_repo_id slipping past the pairing
// check unnoticed: WorkspaceProviderRepositoryPath trims both fields before
// deciding whether they are present, so an untrimmed "   " paired with a
// real provider_scope would pass this validator but still trip the
// "supplied together" clone-path error once trimmed.
func TestApplyRepositoryUpdatesRejectsWhitespaceOnlyRepoIDWithScope(t *testing.T) {
	repository := &models.Repository{ProviderScope: "existing-scope"}
	repoID := "   "

	err := applyRepositoryUpdates(repository, &UpdateRepositoryRequest{ProviderRepoID: &repoID})

	if !errors.Is(err, ErrInvalidRepositorySettings) {
		t.Fatalf("applyRepositoryUpdates error = %v, want ErrInvalidRepositorySettings", err)
	}
}
