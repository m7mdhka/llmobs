// This plugin is a Module Federation REMOTE with NO framework. It exposes "./plugin"
// (its neutral `mount`) and shares ONLY the framework-neutral singletons — the SDK core
// and design tokens. Note what is ABSENT: no react, no react-dom, no react-router-dom, no
// @llmobs/ui. That absence is the whole point of ADR-0030 — a non-React plugin never
// pulls the React binding. publicPath is "auto" so the bundle works wherever the kernel
// serves it.
import { rspack } from "@rspack/core";
import { ModuleFederationPlugin } from "@module-federation/enhanced/rspack";
import { fileURLToPath } from "node:url";
import path from "node:path";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const isProd = process.env.NODE_ENV === "production";

// Framework-neutral singletons only — provided by the host, never bundled here.
const shared = {
  "@llmobs/plugin-sdk": { singleton: true, requiredVersion: false },
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
    uniqueName: "llmobs_plugin_your_vanilla_plugin",
  },
  resolve: {
    extensions: [".ts", ".js", ".json"],
    extensionAlias: { ".js": [".ts", ".js"] },
  },
  module: {
    rules: [
      {
        test: /\.ts$/,
        exclude: /node_modules/,
        loader: "builtin:swc-loader",
        options: {
          jsc: { parser: { syntax: "typescript" } },
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
      name: "your_vanilla_plugin",
      filename: "remoteEntry.js",
      exposes: { "./plugin": "./src/plugin.ts" },
      shared,
    }),
  ],
  experiments: { css: true },
  optimization: { moduleIds: "deterministic" },
  // `make dev`: this plugin runs its own rspack dev server serving remoteEntry.js. Point
  // the kernel's LLMOBS_DEV_PLUGIN_REMOTES at http://localhost:<PLUGIN_PORT>/remoteEntry.js.
  devServer: {
    port: Number(process.env.PLUGIN_PORT ?? 3002),
    hot: true,
    headers: { "Access-Control-Allow-Origin": "*" },
    static: false,
  },
};
