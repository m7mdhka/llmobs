import { test, expect } from "@playwright/test";

// One lean gate for the whole platform slice: log in, discover the Traces nav
// from the plugin manifest, render the traces list (seeded trace), open a trace,
// render its span tree, and open a span's detail panel. Every byte the plugin
// shows arrived through the SDK.

const EMAIL = process.env.LLMOBS_ADMIN_EMAIL ?? "admin@example.com";
const PASSWORD = process.env.LLMOBS_ADMIN_PASSWORD ?? "admin-dev-password";

test("login → manifest nav → traces list → span tree → span panel", async ({ page }) => {
  await page.goto("/");

  // Login form (shell owns auth).
  await page.getByLabel("Email").fill(EMAIL);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();

  // The "Traces" nav item exists ONLY because the tracing plugin's manifest
  // declared it — the shell hardcodes no plugins.
  const tracesNav = page.getByRole("link", { name: "Traces" });
  await expect(tracesNav).toBeVisible();
  await tracesNav.click();

  // The traces list (DSL traces target) renders the seeded trace.
  await expect(page.getByRole("heading", { name: "Traces" })).toBeVisible();
  const firstRow = page.locator("table tbody tr").first();
  await expect(firstRow).toBeVisible();

  // Open the trace → its span tree renders (from the tree endpoint).
  await firstRow.click();
  const spanTree = page.getByRole("tree", { name: "Span tree" });
  await expect(spanTree).toBeVisible();
  const firstSpan = spanTree.getByRole("treeitem").first();
  await expect(firstSpan).toBeVisible();

  // Selecting a span opens its detail panel with the Payloads tab.
  await firstSpan.click();
  await expect(page.getByRole("tab", { name: "Payloads" })).toBeVisible();
});
