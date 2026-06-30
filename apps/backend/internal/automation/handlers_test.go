package automation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"github.com/kandev/kandev/internal/common/logger"
	"github.com/kandev/kandev/internal/events/bus"
	taskmodels "github.com/kandev/kandev/internal/task/models"
	taskrepo "github.com/kandev/kandev/internal/task/repository/sqlite"
	ws "github.com/kandev/kandev/pkg/websocket"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store := setupTestStore(t)
	log, err := logger.NewFromZap(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	eb := bus.NewMemoryEventBus(log)
	return NewService(store, eb, log)
}

func TestCreateAutomationResponse_IncludesWebhookSecret(t *testing.T) {
	// Mirrors the WS create flow: the server should return the plaintext
	// webhook secret exactly once, so the UI can show it to the user.
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	req, err := ws.NewRequest("req-1", ws.ActionAutomationCreate, &CreateAutomationRequest{
		WorkspaceID:       "ws-1",
		Name:              "with secret",
		WorkflowID:        "wf-1",
		WorkflowStepID:    "step-1",
		AgentProfileID:    "agent-1",
		ExecutorProfileID: "exec-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := wsCreate(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeResponse {
		t.Fatalf("expected response, got %v: %s", resp.Type, string(resp.Payload))
	}

	var got CreateAutomationResponse
	if err := json.Unmarshal(resp.Payload, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Automation == nil {
		t.Fatalf("expected automation in response, got %+v", got)
	}
	if got.ID == "" {
		t.Fatalf("expected non-empty automation id, got %+v", got)
	}
	if got.WebhookSecret == "" {
		t.Fatal("expected non-empty webhook secret in create response")
	}
}

func TestWsRevealWebhookSecret_Roundtrip(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	a, err := svc.CreateAutomation(ctx, &CreateAutomationRequest{
		WorkspaceID:       "ws-1",
		Name:              "reveal me",
		WorkflowID:        "wf-1",
		WorkflowStepID:    "step-1",
		AgentProfileID:    "agent-1",
		ExecutorProfileID: "exec-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := ws.NewRequest("req-1", ws.ActionAutomationWebhookRevealSecret, map[string]any{"id": a.ID, "workspace_id": "ws-1"})
	resp, err := wsRevealWebhookSecret(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeResponse {
		t.Fatalf("expected response, got %v: %s", resp.Type, string(resp.Payload))
	}

	var got RevealWebhookSecretResponse
	if err := json.Unmarshal(resp.Payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.WebhookSecret == "" {
		t.Fatal("expected non-empty webhook secret")
	}
	// The reveal must return the same secret that the store generated —
	// otherwise the user's copy from the create response would stop working.
	if got.WebhookSecret != a.WebhookSecret {
		t.Errorf("reveal returned a different secret than create: reveal=%q create=%q", got.WebhookSecret, a.WebhookSecret)
	}
}

func TestWsRevealWebhookSecret_NotFound(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	req, _ := ws.NewRequest("req-1", ws.ActionAutomationWebhookRevealSecret, map[string]any{"id": "does-not-exist", "workspace_id": "ws-1"})
	resp, err := wsRevealWebhookSecret(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeError {
		t.Fatalf("expected error response, got %v: %s", resp.Type, string(resp.Payload))
	}

	var ep ws.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &ep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ep.Code != ws.ErrorCodeNotFound {
		t.Errorf("expected NOT_FOUND, got %q", ep.Code)
	}
}

func TestWsRevealWebhookSecret_RequiresID(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	req, _ := ws.NewRequest("req-1", ws.ActionAutomationWebhookRevealSecret, map[string]any{})
	resp, err := wsRevealWebhookSecret(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeError {
		t.Fatalf("expected error, got %v: %s", resp.Type, string(resp.Payload))
	}
	var ep ws.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &ep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ep.Code != ws.ErrorCodeBadRequest {
		t.Errorf("expected BAD_REQUEST, got %q", ep.Code)
	}
}

func TestWsRevealWebhookSecret_RejectsCrossWorkspace(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	// Create automation in workspace A.
	a, err := svc.CreateAutomation(ctx, &CreateAutomationRequest{
		WorkspaceID:       "ws-A",
		Name:              "workspace A automation",
		WorkflowID:        "wf-1",
		WorkflowStepID:    "step-1",
		AgentProfileID:    "agent-1",
		ExecutorProfileID: "exec-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to reveal using workspace B's id — must return NOT_FOUND, not the secret.
	req, _ := ws.NewRequest("req-1", ws.ActionAutomationWebhookRevealSecret, map[string]any{"id": a.ID, "workspace_id": "ws-B"})
	resp, err := wsRevealWebhookSecret(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeError {
		t.Fatalf("expected error response for cross-workspace reveal, got %v: %s", resp.Type, string(resp.Payload))
	}

	var ep ws.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &ep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ep.Code != ws.ErrorCodeNotFound {
		t.Errorf("expected NOT_FOUND to avoid disclosing existence, got %q", ep.Code)
	}
}

func TestWsDeleteRun_DeletesRun(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	// Create an automation and a run.
	a := &Automation{WorkspaceID: "ws-1", Name: "X", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	run := &AutomationRun{
		AutomationID: a.ID,
		TriggerType:  TriggerTypeScheduled,
		Status:       RunStatusSkipped,
		TriggerData:  json.RawMessage(`{}`),
	}
	if err := svc.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	req, err := ws.NewRequest("req-del", ws.ActionAutomationRunDelete, map[string]string{"run_id": run.ID, "workspace_id": "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteRun(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeResponse {
		t.Fatalf("expected response, got %v: %s", resp.Type, string(resp.Payload))
	}

	// Run should be gone.
	got, _ := svc.store.GetRun(ctx, run.ID)
	if got != nil {
		t.Error("expected run to be deleted")
	}
}

func TestWsDeleteRun_RequiresRunID(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	req, err := ws.NewRequest("req-1", ws.ActionAutomationRunDelete, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteRun(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var ep struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != ws.ErrorCodeBadRequest {
		t.Errorf("expected BAD_REQUEST, got %q", ep.Code)
	}
}

func TestWsDeleteAllRuns_ClearsAllRuns(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	a := &Automation{WorkspaceID: "ws-1", Name: "Y", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := svc.store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID,
			TriggerType:  TriggerTypeScheduled,
			Status:       RunStatusSkipped,
			TriggerData:  json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	req, err := ws.NewRequest("req-all", ws.ActionAutomationRunsDeleteAll, map[string]string{"automation_id": a.ID, "workspace_id": "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteAllRuns(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeResponse {
		t.Fatalf("expected response, got %v: %s", resp.Type, string(resp.Payload))
	}

	runs, _ := svc.store.ListRuns(ctx, a.ID, 50)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs after delete-all, got %d", len(runs))
	}
}

func TestWsDeleteRun_RequiresWorkspaceID(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	req, err := ws.NewRequest("req-1", ws.ActionAutomationRunDelete, map[string]string{"run_id": "some-id"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteRun(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var ep ws.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != ws.ErrorCodeBadRequest {
		t.Errorf("expected BAD_REQUEST for missing workspace_id, got %q", ep.Code)
	}
}

func TestWsDeleteRun_RejectsCrossWorkspace(t *testing.T) {
	svc := newTestService(t)
	deleter := &fakeTaskDeleter{}
	svc.SetTaskDeleter(deleter)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	// Create automation in workspace A.
	a := &Automation{WorkspaceID: "ws-A", Name: "X", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	run := &AutomationRun{
		AutomationID: a.ID,
		TriggerType:  TriggerTypeScheduled,
		Status:       RunStatusSkipped,
		TriggerData:  json.RawMessage(`{}`),
	}
	if err := svc.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Delete with workspace B — must return NOT_FOUND without deleting the run or calling TaskDeleter.
	req, err := ws.NewRequest("req-del", ws.ActionAutomationRunDelete,
		map[string]string{"run_id": run.ID, "workspace_id": "ws-B"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteRun(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeError {
		t.Fatalf("expected error response for cross-workspace delete, got %v", resp.Type)
	}
	var ep ws.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &ep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ep.Code != ws.ErrorCodeNotFound {
		t.Errorf("expected NOT_FOUND, got %q", ep.Code)
	}
	// Run row must still exist.
	got, _ := svc.store.GetRun(ctx, run.ID)
	if got == nil {
		t.Error("cross-workspace delete must not remove the run row")
	}
	// TaskDeleter must never have been called.
	if len(deleter.deleted) != 0 {
		t.Errorf("TaskDeleter must not be called on cross-workspace reject, got calls: %v", deleter.deleted)
	}
}

func TestWsDeleteAllRuns_RequiresWorkspaceID(t *testing.T) {
	svc := newTestService(t)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	req, err := ws.NewRequest("req-1", ws.ActionAutomationRunsDeleteAll, map[string]string{"automation_id": "some-id"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteAllRuns(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var ep ws.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != ws.ErrorCodeBadRequest {
		t.Errorf("expected BAD_REQUEST for missing workspace_id, got %q", ep.Code)
	}
}

func TestWsDeleteAllRuns_RejectsCrossWorkspace(t *testing.T) {
	svc := newTestService(t)
	deleter := &fakeTaskDeleter{}
	svc.SetTaskDeleter(deleter)
	log, _ := logger.NewFromZap(zap.NewNop())
	ctx := context.Background()

	// Create automation in workspace A with a run.
	a := &Automation{WorkspaceID: "ws-A", Name: "Y", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	run := &AutomationRun{
		AutomationID: a.ID,
		TriggerType:  TriggerTypeScheduled,
		Status:       RunStatusSkipped,
		TriggerData:  json.RawMessage(`{}`),
	}
	if err := svc.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Delete-all with workspace B — must return NOT_FOUND without deleting runs or calling TaskDeleter.
	req, err := ws.NewRequest("req-all", ws.ActionAutomationRunsDeleteAll,
		map[string]string{"automation_id": a.ID, "workspace_id": "ws-B"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := wsDeleteAllRuns(svc, log)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != ws.MessageTypeError {
		t.Fatalf("expected error response for cross-workspace delete-all, got %v", resp.Type)
	}
	var ep ws.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &ep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ep.Code != ws.ErrorCodeNotFound {
		t.Errorf("expected NOT_FOUND, got %q", ep.Code)
	}
	// Run rows must still exist.
	runs, _ := svc.store.ListRuns(ctx, a.ID, 50)
	if len(runs) == 0 {
		t.Error("cross-workspace delete-all must not remove run rows")
	}
	// TaskDeleter must never have been called.
	if len(deleter.deleted) != 0 {
		t.Errorf("TaskDeleter must not be called on cross-workspace reject, got calls: %v", deleter.deleted)
	}
}

// fakeTaskDeleter records deletions and can inject errors per task ID.
type fakeTaskDeleter struct {
	deleted          []string
	errors           map[string]error
	tasksForDeletion []*taskmodels.Task
	published        []string
	cleaned          []string
}

func (f *fakeTaskDeleter) DeleteTask(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if f.errors != nil {
		if err, ok := f.errors[id]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeTaskDeleter) GetTasksForDeletion(_ context.Context, ids []string) ([]*taskmodels.Task, error) {
	byID := make(map[string]*taskmodels.Task, len(f.tasksForDeletion))
	for _, task := range f.tasksForDeletion {
		byID[task.ID] = task
	}
	tasks := make([]*taskmodels.Task, 0, len(ids))
	for _, id := range ids {
		if task := byID[id]; task != nil {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (f *fakeTaskDeleter) PublishTaskDeleted(_ context.Context, task *taskmodels.Task) {
	f.published = append(f.published, task.ID)
}

func (f *fakeTaskDeleter) CleanupTaskResources(_ context.Context, taskID string, _ bool) {
	f.cleaned = append(f.cleaned, taskID)
}

func TestService_DeleteRun_CallsTaskDeleter(t *testing.T) {
	svc := newTestService(t)
	deleter := &fakeTaskDeleter{}
	svc.SetTaskDeleter(deleter)
	ctx := context.Background()

	a := &Automation{WorkspaceID: "ws-1", Name: "A", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	run := &AutomationRun{
		AutomationID: a.ID,
		TriggerType:  TriggerTypeScheduled,
		Status:       RunStatusTaskCreated,
		TaskID:       "task-xyz",
		TriggerData:  json.RawMessage(`{}`),
	}
	if err := svc.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}

	// Task deleter must have been called.
	if len(deleter.deleted) != 1 || deleter.deleted[0] != "task-xyz" {
		t.Errorf("expected DeleteTask(task-xyz), got %v", deleter.deleted)
	}
	// Run row must be gone.
	got, _ := svc.store.GetRun(ctx, run.ID)
	if got != nil {
		t.Error("expected run row to be removed")
	}
}

func TestService_DeleteRun_TaskNotFound_StillDeletesRun(t *testing.T) {
	svc := newTestService(t)
	deleter := &fakeTaskDeleter{
		errors: map[string]error{"task-gone": taskrepo.ErrTaskNotFound},
	}
	svc.SetTaskDeleter(deleter)
	ctx := context.Background()

	a := &Automation{WorkspaceID: "ws-1", Name: "B", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	run := &AutomationRun{
		AutomationID: a.ID,
		TriggerType:  TriggerTypeScheduled,
		Status:       RunStatusSkipped,
		TaskID:       "task-gone",
		TriggerData:  json.RawMessage(`{}`),
	}
	if err := svc.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Must succeed even though the task is not found.
	if err := svc.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("DeleteRun with not-found task: %v", err)
	}

	// Run row must still be gone.
	got, _ := svc.store.GetRun(ctx, run.ID)
	if got != nil {
		t.Error("expected run row to be removed despite task-not-found")
	}
}

func TestService_DeleteAllRuns_CallsTaskDeleterForEach(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.store.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, title TEXT, state TEXT)`); err != nil {
		t.Fatal("create tasks table:", err)
	}
	svc.SetTaskDeleter(&sqliteTaskDeleter{db: svc.store.db})

	a := &Automation{WorkspaceID: "ws-1", Name: "C", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	taskIDs := []string{"task-1", "task-2", "task-3"}
	for _, tid := range taskIDs {
		if _, err := svc.store.db.ExecContext(ctx,
			`INSERT INTO tasks (id, title, state) VALUES (?, 'Test task', 'running')`, tid); err != nil {
			t.Fatal("insert task:", err)
		}
		if err := svc.store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID,
			TriggerType:  TriggerTypeScheduled,
			Status:       RunStatusTaskCreated,
			TaskID:       tid,
			TriggerData:  json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Also one run with no task_id (fire-and-forget / skipped).
	if err := svc.store.CreateRun(ctx, &AutomationRun{
		AutomationID: a.ID,
		TriggerType:  TriggerTypeScheduled,
		Status:       RunStatusSkipped,
		TriggerData:  json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteAllRuns(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAllRuns: %v", err)
	}

	var taskCount int
	if err := svc.store.db.GetContext(ctx, &taskCount, `SELECT COUNT(*) FROM tasks`); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Errorf("expected 0 task rows, got %d", taskCount)
	}
	// All run rows gone.
	runs, _ := svc.store.ListRuns(ctx, a.ID, 50)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestService_DeleteAllRuns_PublishesTaskEventsAndCleansResources(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.store.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, title TEXT, state TEXT)`); err != nil {
		t.Fatal("create tasks table:", err)
	}
	deleter := &fakeTaskDeleter{
		tasksForDeletion: []*taskmodels.Task{
			{ID: "task-1", WorkspaceID: "ws-1", WorkflowID: "wf-1", WorkflowStepID: "s-1", Title: "Task 1"},
			{ID: "task-2", WorkspaceID: "ws-1", WorkflowID: "wf-1", WorkflowStepID: "s-1", Title: "Task 2"},
		},
	}
	svc.SetTaskDeleter(deleter)

	a := &Automation{WorkspaceID: "ws-1", Name: "Events", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, tid := range []string{"task-1", "task-2"} {
		if _, err := svc.store.db.ExecContext(ctx,
			`INSERT INTO tasks (id, title, state) VALUES (?, 'Test task', 'running')`, tid); err != nil {
			t.Fatal("insert task:", err)
		}
		if err := svc.store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID,
			TriggerType:  TriggerTypeScheduled,
			Status:       RunStatusTaskCreated,
			TaskID:       tid,
			TriggerData:  json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.DeleteAllRuns(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAllRuns: %v", err)
	}
	for i, id := range []string{"task-1", "task-2"} {
		if deleter.published[i] != id {
			t.Fatalf("published task %d = %q, want %q", i, deleter.published[i], id)
		}
		if deleter.cleaned[i] != id {
			t.Fatalf("cleaned task %d = %q, want %q", i, deleter.cleaned[i], id)
		}
	}
}

func TestService_DeleteAllRuns_TaskNotFound_StillClearsRuns(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.store.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, title TEXT, state TEXT)`); err != nil {
		t.Fatal("create tasks table:", err)
	}
	svc.SetTaskDeleter(&sqliteTaskDeleter{db: svc.store.db})

	a := &Automation{WorkspaceID: "ws-1", Name: "D", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := svc.store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.db.ExecContext(ctx,
		`INSERT INTO tasks (id, title, state) VALUES ('task-ok', 'Test task', 'running')`); err != nil {
		t.Fatal("insert task:", err)
	}
	for _, tid := range []string{"task-stale", "task-ok"} {
		if err := svc.store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID,
			TriggerType:  TriggerTypeScheduled,
			Status:       RunStatusTaskCreated,
			TaskID:       tid,
			TriggerData:  json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.DeleteAllRuns(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAllRuns with not-found task: %v", err)
	}

	runs, _ := svc.store.ListRuns(ctx, a.ID, 50)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs after delete-all, got %d", len(runs))
	}
	var taskCount int
	if err := svc.store.db.GetContext(ctx, &taskCount, `SELECT COUNT(*) FROM tasks`); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Errorf("expected stale/missing task to be ignored and task-ok to be deleted, got %d rows", taskCount)
	}
}

func TestService_DeleteAllRuns_TaskDeleteFailureRollsBack(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	if _, err := store.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, title TEXT, state TEXT)`); err != nil {
		t.Fatal("create tasks table:", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER fail_task_b_delete
		BEFORE DELETE ON tasks
		WHEN OLD.id = 'task-b'
		BEGIN
			SELECT RAISE(ABORT, 'task-b delete failed');
		END;
	`); err != nil {
		t.Fatal("create failing trigger:", err)
	}

	log, _ := logger.NewFromZap(zap.NewNop())
	eb := bus.NewMemoryEventBus(log)
	svc := NewService(store, eb, log)
	svc.SetTaskDeleter(&sqliteTaskDeleter{db: store.db})

	a := &Automation{WorkspaceID: "ws-1", Name: "Rollback", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, tid := range []string{"task-a", "task-b"} {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO tasks (id, title, state) VALUES (?, 'Test task', 'running')`, tid); err != nil {
			t.Fatal("insert task:", err)
		}
		if err := store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID,
			TriggerType:  TriggerTypeScheduled,
			Status:       RunStatusTaskCreated,
			TaskID:       tid,
			TriggerData:  json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.DeleteAllRuns(ctx, a.ID); err == nil {
		t.Fatal("DeleteAllRuns succeeded despite task delete failure")
	}

	runs, err := store.ListRuns(ctx, a.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected both run rows to remain after rollback, got %d", len(runs))
	}
	var taskCount int
	if err := store.db.GetContext(ctx, &taskCount, `SELECT COUNT(*) FROM tasks`); err != nil {
		t.Fatal(err)
	}
	if taskCount != 2 {
		t.Fatalf("expected both task rows to remain after rollback, got %d", taskCount)
	}
}

// TestDeleteAllRuns_AutomationSurvives is a regression guard: deleting all run
// rows — including issuing real DELETE SQL against task rows in the shared
// in-memory DB — must never delete the parent automation row. A real DB-level
// deleter catches SQL trigger / ON DELETE CASCADE regressions. Note: event
// handler side-effects are not covered here (no orchestrator runs in this test).
func TestDeleteAllRuns_AutomationSurvives(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	// Create a minimal tasks table in the same in-memory DB so the
	// real-deleter can insert and then DELETE task rows.
	if _, err := store.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, title TEXT, state TEXT)`); err != nil {
		t.Fatal("create tasks table:", err)
	}

	log, _ := logger.NewFromZap(zap.NewNop())
	eb := bus.NewMemoryEventBus(log)
	svc := NewService(store, eb, log)
	svc.SetTaskDeleter(&sqliteTaskDeleter{db: store.db})

	a := &Automation{WorkspaceID: "ws-1", Name: "Survives", WorkflowID: "wf-1", WorkflowStepID: "s-1", Enabled: true}
	if err := store.CreateAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}

	// Insert real task rows and create runs referencing them.
	taskIDs := []string{"task-a", "task-b", "task-c"}
	for _, tid := range taskIDs {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO tasks (id, title, state) VALUES (?, 'Test task', 'running')`, tid); err != nil {
			t.Fatal("insert task:", err)
		}
		if err := store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID,
			TriggerType:  TriggerTypeScheduled,
			Status:       RunStatusTaskCreated,
			TaskID:       tid,
			TriggerData:  json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Skipped runs without task IDs.
	for range 3 {
		if err := store.CreateRun(ctx, &AutomationRun{
			AutomationID: a.ID, TriggerType: TriggerTypeScheduled,
			Status: RunStatusSkipped, TriggerData: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.DeleteAllRuns(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAllRuns: %v", err)
	}

	// Automation row must still exist after real task DELETEs fired.
	got, err := store.GetAutomation(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAutomation after DeleteAllRuns: %v", err)
	}
	if got == nil {
		t.Error("automation was deleted by DeleteAllRuns — regression")
		return
	}
	if got.Name != "Survives" {
		t.Errorf("unexpected automation name %q", got.Name)
	}

	// Runs must be gone.
	runs, _ := store.ListRuns(ctx, a.ID, 50)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
	var taskCount int
	if err := store.db.GetContext(ctx, &taskCount, `SELECT COUNT(*) FROM tasks`); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Errorf("expected 0 task rows, got %d", taskCount)
	}
}

// sqliteTaskDeleter deletes from the real tasks table in the same in-memory
// DB, so any SQL trigger or ON DELETE CASCADE that touches automations fires.
type sqliteTaskDeleter struct {
	db      *sqlx.DB
	deleted []string
}

func (d *sqliteTaskDeleter) DeleteTask(ctx context.Context, id string) error {
	d.deleted = append(d.deleted, id)
	_, err := d.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	return err
}
