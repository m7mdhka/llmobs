// The tracing plugin is a Module Federation REMOTE. It exposes "./plugin" (the
// Traces surface) and shares the host's singletons — it must never bundle its own
// react/router/sdk/design-system. PUBLIC_PATH is "auto" so the bundle works from
// wherever the kernel serves it (registry asset path).
import { rspack } from "@rspack/core";
import { ModuleFederationPlugin } from "@module-federation/enhanced/rspack";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createRequire } from "node:module";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const pkg = require("./package.json");
const isProd = process.env.NODE_ENV === "production";

// Shared singletons — provided by the host, never bundled here.
const shared = {
  react: { singleton: true, requiredVersion: pkg.dependencies.react },
  "react-dom": { singleton: true, requiredVersion: pkg.dependencies["react-dom"] },
  "react-router-dom": { singleton: true, requiredVersion: pkg.dependencies["react-router-dom"] },
  "@llmobs/plugin-sdk": { singleton: true, requiredVersion: false },
  "@llmobs/ui": { singleton: true, requiredVersion: false },
  "@llmobs/tokens": { singleton: true, requiredVersion: false },
};

export default {
  context: __dirname,
  entry: {},
  mode: isProd ? "production" : "development",
  devtool: isProd ? false : "source-map",
  output: {
    path: path.resolve(__dirname, "dist"),
    publicPath: "auto",
    clean: true,
    uniqueName: "llmobs_plugin_your_plugin",
  },
  resolve: {
    extensions: [".ts", ".tsx", ".js", ".jsx", ".json"],
    extensionAlias: { ".js": [".ts", ".tsx", ".js"] },
  },
  module: {
    rules: [
      {
        test: /\.[jt]sx?$/,
        exclude: /node_modules/,
        loader: "builtin:swc-loader",
        options: {
          jsc: { parser: { syntax: "typescript", tsx: true }, transform: { react: { runtime: "automatic" } } },
        },
      },
      { test: /\.css$/, type: "css" },
    ],
  },
  plugins: [
    new rspack.DefinePlugin({
      "process.env.NODE_ENV": JSON.stringify(isProd ? "production" : "development"),
    }),
    new ModuleFederationPlugin({
      name: "your_plugin",
      filename: "remoteEntry.js",
      exposes: { "./plugin": "./src/plugin.tsx" },
      shared,
    }),
  ],
  experiments: { css: true },
  optimization: { moduleIds: "deterministic" },
};
