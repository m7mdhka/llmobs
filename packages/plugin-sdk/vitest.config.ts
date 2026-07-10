import { defineConfig } from "vitest/config";

// Self-tests for the SDK's hooks + the @llmobs/plugin-sdk/testing utilities, run in a
// jsdom DOM so React hooks render. These prove the test utilities a plugin author
// uses actually work.
export default defineConfig({
  test: {
    environment: "jsdom",
    include: ["test/**/*.test.tsx"],
    globals: true,
  },
});
