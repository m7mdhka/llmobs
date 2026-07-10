// The shell is a Module Federation 2.0 HOST. It declares the shared singletons
// (react, react-dom, router, the SDK, tokens, ui) and loads plugin remotes at
// runtime from the registry — it hardcodes zero remotes here. Base path is
// injected via PUBLIC_PATH so subpath hosting works (the kernel serves the shell
// under "/"; a reverse proxy may mount it elsewhere).
import { rspack } from "@rspack/core";
import { ModuleFederationPlugin } from "@module-federation/enhanced/rspack";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createRequire } from "node:module";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const pkg = require("./package.json");

// Runtime-resolved base path. "auto" lets the browser infer it from the loading
// <script> so the same bundle works at "/" or any subpath.
const PUBLIC_PATH = process.env.PUBLIC_PATH ?? "auto";
const isProd = process.env.NODE_ENV === "production";

// The singletons every plugin must share with the host — never duplicated.
const singletons = {
  react: { singleton: true, requiredVersion: pkg.dependencies.react, eager: true },
  "react-dom": { singleton: true, requiredVersion: pkg.dependencies["react-dom"], eager: true },
  "react-router-dom": { singleton: true, requiredVersion: pkg.dependencies["react-router-dom"], eager: true },
  // Workspace packages use "workspace:*" specifiers, which are not semver ranges;
  // requiredVersion:false tells MF to treat them as a plain singleton by name.
  "@llmobs/tokens": { singleton: true, eager: true, requiredVersion: false },
  "@llmobs/ui": { singleton: true, eager: true, requiredVersion: false },
  "@llmobs/plugin-sdk": { singleton: true, eager: true, requiredVersion: false },
};

export default {
  context: __dirname,
  entry: { main: "./src/index.ts" },
  mode: isProd ? "production" : "development",
  devtool: isProd ? false : "source-map",
  output: {
    path: path.resolve(__dirname, "dist"),
    publicPath: PUBLIC_PATH,
    clean: true,
    filename: isProd ? "[name].[contenthash].js" : "[name].js",
    uniqueName: "llmobs_shell",
  },
  resolve: {
    extensions: [".ts", ".tsx", ".js", ".jsx", ".json"],
    // Source uses ESM-style ".js" specifiers (consistent with the tsc-built
    // packages); map them back to the TS sources for bundling.
    extensionAlias: { ".js": [".ts", ".tsx", ".js"] },
  },
  module: {
    rules: [
      {
        test: /\.[jt]sx?$/,
        exclude: /node_modules/,
        loader: "builtin:swc-loader",
        options: {
          jsc: {
            parser: { syntax: "typescript", tsx: true },
            transform: { react: { runtime: "automatic" } },
          },
        },
      },
      { test: /\.css$/, type: "css" },
    ],
  },
  plugins: [
    new rspack.HtmlRspackPlugin({
      template: "./src/index.html",
    }),
    new rspack.DefinePlugin({
      "process.env.NODE_ENV": JSON.stringify(isProd ? "production" : "development"),
    }),
    new ModuleFederationPlugin({
      name: "shell",
      // No static remotes: the registry supplies them at runtime (see remoteLoader).
      shared: singletons,
    }),
  ],
  experiments: { css: true },
  devServer: {
    port: 3000,
    historyApiFallback: true,
    hot: true,
  },
  optimization: {
    // Deterministic module/chunk ids so the singleton check can compare builds.
    moduleIds: "deterministic",
  },
};
