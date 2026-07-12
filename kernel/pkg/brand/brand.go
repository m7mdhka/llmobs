// Package brand is the ONE place the product name lives in Go. Never
// hardcode the product name, env prefix, or table/namespace prefixes elsewhere —
// derive them from here. A rename changes this package (and its TS twin,
// packages/brand) and nothing else.
package brand

// Name is the product name.
const Name = "LLMObs"

// EnvPrefix prefixes every environment variable the kernel reads (12-factor).
const EnvPrefix = "LLMOBS_"

// Env returns the environment variable name for a config key,
// e.g. Env("DATABASE_URL") -> "LLMOBS_DATABASE_URL".
func Env(key string) string { return EnvPrefix + key }
