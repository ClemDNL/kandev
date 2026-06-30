import { test, expect } from "../fixtures/test-base";

test.describe("Automations settings on mobile", () => {
  test("confirms before deleting a recent run", async ({ testPage, seedData, apiClient }) => {
    const automation = await apiClient.seedAutomation({
      workspaceId: seedData.workspaceId,
      name: "Mobile Run Delete Test",
      workflowId: seedData.workflowId,
      workflowStepId: seedData.startStepId,
    });
    const task = await apiClient.createTask(seedData.workspaceId, "Mobile automation run task", {
      workflow_id: seedData.workflowId,
      workflow_step_id: seedData.startStepId,
    });
    await apiClient.seedAutomationRun(automation.id, "task_created", { taskId: task.id });

    await testPage.goto(`/settings/workspace/${seedData.workspaceId}/automations/${automation.id}`);
    await testPage.getByTestId("automation-editor").waitFor({ state: "visible", timeout: 15_000 });

    const scrollContainer = testPage.getByTestId("settings-scroll-container");
    await scrollContainer.evaluate((el) => (el.scrollTop = el.scrollHeight));

    await testPage.locator("button", { hasText: /Recent Runs/ }).click();
    const row = testPage.locator("table tbody tr").first();
    await expect(row).toBeVisible();

    await row.getByTestId("delete-run").click();
    await expect(testPage.getByRole("alertdialog", { name: "Delete task" })).toBeVisible();
    await testPage.getByTestId("delete-run-confirm").click();
    await expect(testPage.getByText("No runs yet")).toBeVisible({ timeout: 5_000 });
  });
});
