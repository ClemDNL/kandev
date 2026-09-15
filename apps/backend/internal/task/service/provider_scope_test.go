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

// TestApplyRepositoryUpdatesRejectsUnpairedProviderRepoID guards
// repoclone.Cloner.WorkspaceProviderRepositoryPath's invariant: it errors if
// exactly one of provider_scope/provider_repo_id is set. Persisting only one
// left a repository row unusable at the next task session's workspace setup
// ("provider scope and repository ID must be supplied together").
func TestApplyRepositoryUpdatesRejectsUnpairedProviderRepoID(t *testing.T) {
	repository := &models.Repository{}
	repoID := "1131388506"

	err := applyRepositoryUpdates(repository, &UpdateRepositoryRequest{ProviderRepoID: &repoID})

	if !errors.Is(err, ErrInvalidRepositorySettings) {
		t.Fatalf("applyRepositoryUpdates error = %v, want ErrInvalidRepositorySettings", err)
	}
}
