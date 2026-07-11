import { defineConfig } from "vitest/config";

// The vanilla plugin renders with DOM APIs, so the falsification test needs a DOM — but
// NO React. jsdom provides `document`; there is deliberately no @testing-library/react.
export default defineConfig({
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
  },
});
