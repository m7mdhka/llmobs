// Command mcp-server is the first-party LLMObs MCP server: it exposes read-only,
// metadata-safe tools over the Query API so an AI assistant can reason about your
// LLM system ("why did checkout-agent get slow yesterday?"). It speaks JSON-RPC
// 2.0 over stdio (the standard MCP transport).
//
// Safety by construction: it requires a metadata-scoped API key. If the key can
// read payloads it REFUSES TO START unless --allow-payloads acknowledges the
// exposure — an assistant must not silently ingest prompts/PII.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

const protocolVersion = "2024-11-05"

type server struct {
	client *llmobsClient
	out    *json.Encoder
}

func main() {
	baseURL := envOr("LLMOBS_URL", "http://localhost:8080")
	apiKey := os.Getenv("LLMOBS_API_KEY")
	allowPayloads := flag.Bool("allow-payloads", false, "permit a payload-scoped key (exposes prompts/PII to the assistant)")
	flag.Parse()

	if apiKey == "" {
		fatal("LLMOBS_API_KEY is required")
	}
	client := newClient(baseURL, apiKey)

	// Verify the key's scope before serving (the safety posture, in code).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	who, err := client.whoami(ctx)
	cancel()
	if err != nil {
		fatal("could not verify credentials against %s: %v", baseURL, err)
	}
	if who.ReadPayloads && !*allowPayloads {
		fatal("refusing to start: the API key has payload access (query:payloads). " +
			"Use a metadata-scoped key, or pass --allow-payloads to expose prompts/PII to the assistant.")
	}

	s := &server{client: client, out: json.NewEncoder(os.Stdout)}
	s.serve()
}

// serve runs the JSON-RPC stdio loop (newline-delimited messages).
func (s *server) serve() {
	dec := json.NewDecoder(bufio.NewReader(os.Stdin))
	for {
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			return // EOF or malformed stream: exit
		}
		s.handle(&req)
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) handle(req *rpcRequest) {
	// Notifications (no id) get no response.
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		s.reply(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo":      map[string]any{"name": "llmobs-mcp", "version": "0.1.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		})
	case "notifications/initialized", "notifications/cancelled":
		// no-op
	case "ping":
		s.reply(req.ID, map[string]any{})
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		s.handleToolCall(req)
	default:
		if !isNotification {
			s.replyError(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func (s *server) handleToolCall(req *rpcRequest) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyError(req.ID, -32602, "invalid params")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	text, err := s.callTool(ctx, p.Name, p.Arguments)
	if err != nil {
		// MCP convention: tool errors are a result with isError, not a protocol error.
		s.reply(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "error: " + err.Error()}},
			"isError": true,
		})
		return
	}
	s.reply(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

func (s *server) reply(id json.RawMessage, result any) {
	if len(id) == 0 {
		return
	}
	_ = s.out.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (s *server) replyError(id json.RawMessage, code int, msg string) {
	_ = s.out.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": rpcError{Code: code, Message: msg}})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "llmobs-mcp: "+format+"\n", a...)
	os.Exit(1)
}
