import { defineConfig, devices } from "@playwright/test";

// The base URL is the running lite stack (kernel serving the shell). The e2e
// script (scripts/e2e-web.sh) brings the stack up and seeds a trace first.
const PORT = process.env.LLMOBS_API_PORT ?? "18080";

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: "off",
    headless: true,
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
