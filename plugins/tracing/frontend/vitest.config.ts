import { defineConfig } from "vitest/config";

// buildTree and friends are pure functions — a node env (no jsdom) suffices.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
