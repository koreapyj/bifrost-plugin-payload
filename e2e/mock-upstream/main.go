// Command mock-upstream is a minimal OpenAI- and Anthropic-compatible upstream
// used by the end-to-end test (e2e/run.sh). It records every request it
// receives (method, path, headers and the raw body) so the test can assert on
// the exact bytes Bifrost sent to the provider.
//
// Endpoints (any path prefix is accepted, so a provider base_url may carry a
// path such as http://127.0.0.1:PORT/__fail):
//
//	POST .../v1/chat/completions   OpenAI chat completion
//	POST .../v1/responses          OpenAI Responses API
//	POST .../v1/embeddings         OpenAI embeddings
//	POST .../v1/messages           Anthropic messages
//	GET  .../v1/models             model list (OpenAI or Anthropic shape)
//	GET  /__records                recorded requests as a JSON array
//	DELETE /__records              clear recorded requests
//	GET  /__health                 readiness probe
//
// Forced failure: a request answers HTTP 500 when its path contains the
// "/__fail/" segment or when its API key (Authorization: Bearer, x-api-key or
// api-key) is listed in -fail-keys. Failing requests are still recorded.
//
// Only the Go standard library is used.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Record is one request received by the mock.
type Record struct {
	Seq     int                 `json:"seq"`
	Time    string              `json:"time"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Headers map[string][]string `json:"headers"`
	APIKey  string              `json:"api_key,omitempty"`
	Failed  bool                `json:"failed"`
	// BodyRaw holds the exact request body bytes.
	BodyRaw string `json:"body_raw"`
	// Body holds the parsed body when it is valid JSON, otherwise null.
	Body json.RawMessage `json:"body"`
}

type recorder struct {
	mu      sync.Mutex
	seq     int
	records []Record
	file    *os.File
}

func (r *recorder) add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rec.Seq = r.seq
	r.records = append(r.records, rec)
	if r.file != nil {
		line, err := json.Marshal(rec)
		if err == nil {
			line = append(line, '\n')
			if _, err := r.file.Write(line); err != nil {
				log.Printf("write record: %v", err)
			}
		}
	}
}

func (r *recorder) list() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.records))
	copy(out, r.records)
	return out
}

func (r *recorder) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = nil
}

type server struct {
	rec      *recorder
	failKeys map[string]bool
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	recordPath := flag.String("record", "", "append every request as a JSON line to this file")
	portFile := flag.String("port-file", "", "write the bound port to this file once listening")
	failKeys := flag.String("fail-keys", "", "comma-separated API keys that always receive HTTP 500")
	flag.Parse()

	s := &server{rec: &recorder{}, failKeys: map[string]bool{}}
	for _, k := range strings.Split(*failKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			s.failKeys[k] = true
		}
	}
	if *recordPath != "" {
		f, err := os.OpenFile(*recordPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("open record file: %v", err)
		}
		defer f.Close()
		s.rec.file = f
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if *portFile != "" {
		if err := os.WriteFile(*portFile, []byte(fmt.Sprintf("%d\n", port)), 0o644); err != nil {
			log.Fatalf("write port file: %v", err)
		}
	}
	log.Printf("mock-upstream listening on %s", ln.Addr())
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__health":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	case "/__records":
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.rec.list())
		case http.MethodDelete:
			s.rec.clear()
			writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key := apiKey(r)
	anthropic := r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" ||
		strings.HasSuffix(r.URL.Path, "/v1/messages")
	failed := strings.Contains(r.URL.Path, "/__fail/") || s.failKeys[key]

	rec := Record{
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: map[string][]string(r.Header.Clone()),
		APIKey:  key,
		Failed:  failed,
		BodyRaw: string(body),
		Body:    json.RawMessage("null"),
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Valid(body) {
		rec.Body = json.RawMessage(body)
	}
	s.rec.add(rec)

	if failed && r.Method == http.MethodPost {
		if anthropic {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "api_error", "message": "mock-upstream forced failure"},
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"message": "mock-upstream forced failure", "type": "server_error", "code": "mock_failure"},
		})
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &req)
	model := req.Model
	if model == "" {
		model = "mock-model"
	}
	now := time.Now().Unix()

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/models"):
		if anthropic {
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"type": "model", "id": "claude-mock", "display_name": "Claude Mock",
					"created_at": "2025-01-01T00:00:00Z",
				}},
				"has_more": false, "first_id": "claude-mock", "last_id": "claude-mock",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "gpt-mock", "object": "model", "created": now, "owned_by": "mock"},
			},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
		writeJSON(w, http.StatusOK, map[string]any{
			"id": fmt.Sprintf("chatcmpl-mock-%d", now), "object": "chat.completion", "created": now, "model": model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "mock reply"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/responses"):
		writeJSON(w, http.StatusOK, map[string]any{
			"id": fmt.Sprintf("resp_mock_%d", now), "object": "response", "created_at": now,
			"status": "completed", "model": model,
			"output": []any{map[string]any{
				"type": "message", "id": "msg_mock", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "mock reply", "annotations": []any{}}},
			}},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/embeddings"):
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list", "model": model,
			"data":  []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
			"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/messages"):
		writeJSON(w, http.StatusOK, map[string]any{
			"id": fmt.Sprintf("msg_mock_%d", now), "type": "message", "role": "assistant", "model": model,
			"content":       []any{map[string]any{"type": "text", "text": "mock reply"}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"message": "mock-upstream: unsupported route " + r.Method + " " + r.URL.Path},
		})
	}
}

// apiKey extracts the credential the provider client sent.
func apiKey(r *http.Request) string {
	if v := r.Header.Get("x-api-key"); v != "" {
		return v
	}
	if v := r.Header.Get("api-key"); v != "" {
		return v
	}
	if v := r.Header.Get("Authorization"); len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}
