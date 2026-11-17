package rpg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openAICompletionServer returns an httptest server speaking the
// OpenAI-compatible chat completion shape the extractor consumes, replying
// with a fixed content string.
func openAICompletionServer(t *testing.T, content string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer key", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "test-model" {
			t.Errorf("model = %q, want test-model", body.Model)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
			t.Errorf("messages = %+v, want system+user pair", body.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": content}},
			},
		})
	}))
}

func llmExtractorFor(endpoint string) *LLMExtractor {
	return NewLLMExtractor(LLMExtractorConfig{
		Provider: "openai",
		Model:    "test-model",
		Endpoint: endpoint,
		APIKey:   "test-key",
		Timeout:  2 * time.Second,
	})
}

// TestLLMExtractorWire covers the real request/response path of the LLM
// feature extractor against an httptest OpenAI-compatible server: atomic
// features (JSON and fenced variants), primary feature mapping, summaries,
// and the local fallback on transport and protocol failures.
func TestLLMExtractorWire(t *testing.T) {
	ctx := context.Background()

	t.Run("atomic_features_json", func(t *testing.T) {
		srv := openAICompletionServer(t, `["validate token", "load config"]`, http.StatusOK)
		defer srv.Close()
		e := llmExtractorFor(srv.URL)
		if e.Mode() != "llm" {
			t.Fatalf("Mode = %q, want llm", e.Mode())
		}
		features := e.ExtractAtomicFeatures(ctx, "AuthManager.validate", "func validate()", "AuthManager", "checks tokens")
		if len(features) != 2 || features[0] != "validate token" {
			t.Fatalf("features = %v, want two atomic phrases", features)
		}
	})

	t.Run("atomic_features_fenced", func(t *testing.T) {
		srv := openAICompletionServer(t, "```json\n[\"parse request\"]\n```", http.StatusOK)
		defer srv.Close()
		features := llmExtractorFor(srv.URL).ExtractAtomicFeatures(ctx, "parse", "", "", "")
		if len(features) != 1 || features[0] != "parse request" {
			t.Fatalf("features = %v, want fenced JSON parsed", features)
		}
	})

	t.Run("extract_feature_primary", func(t *testing.T) {
		srv := openAICompletionServer(t, `["rotate session key"]`, http.StatusOK)
		defer srv.Close()
		label := llmExtractorFor(srv.URL).ExtractFeature(ctx, "rotate", "", "", "")
		if label == "" || strings.Contains(label, "json") {
			t.Fatalf("label = %q, want primary feature from LLM output", label)
		}
	})

	t.Run("generate_summary", func(t *testing.T) {
		srv := openAICompletionServer(t, "manages player finances", http.StatusOK)
		defer srv.Close()
		summary, err := llmExtractorFor(srv.URL).GenerateSummary(ctx, "FinanceManager", "area context")
		if err != nil || summary != "manages player finances" {
			t.Fatalf("summary = %q err=%v", summary, err)
		}
	})

	t.Run("http_error_falls_back_to_local", func(t *testing.T) {
		srv := openAICompletionServer(t, "ignored", http.StatusInternalServerError)
		defer srv.Close()
		e := llmExtractorFor(srv.URL)
		features := e.ExtractAtomicFeatures(ctx, "FinanceManager.addCoins", "func addCoins(n int)", "FinanceManager", "adds coins")
		if len(features) == 0 {
			t.Fatal("expected local fallback features on HTTP 500")
		}
		label := e.ExtractFeature(ctx, "addCoins", "", "", "")
		if label == "" {
			t.Fatal("expected local fallback label on HTTP 500")
		}
	})

	t.Run("transport_error_falls_back_to_local", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // closed server => connection refused
		features := llmExtractorFor(srv.URL).ExtractAtomicFeatures(ctx, "load", "", "", "")
		if len(features) == 0 {
			t.Fatal("expected local fallback features on transport failure")
		}
	})

	t.Run("empty_content_falls_back", func(t *testing.T) {
		srv := openAICompletionServer(t, "", http.StatusOK)
		defer srv.Close()
		features := llmExtractorFor(srv.URL).ExtractAtomicFeatures(ctx, "load", "", "", "")
		if len(features) == 0 {
			t.Fatal("expected local fallback features on empty LLM content")
		}
	})
}

// TestParseAtomicFeatureResponse pins the response parsing contract
// independently of the wire: fenced JSON, plain arrays, separator-split
// fallbacks, dedupe/cap and empty input.
func TestParseAtomicFeatureResponseShapes(t *testing.T) {
	if got := parseAtomicFeatureResponse(""); got != nil {
		t.Fatalf("empty input = %v, want nil", got)
	}
	if got := parseAtomicFeatureResponse(`["a","a","b","c","d","e","f"]`); len(got) != 5 || got[0] != "a" {
		t.Fatalf("dedupe/cap = %v, want 5 unique", got)
	}
	if got := parseAtomicFeatureResponse("- load save\n- save game; quit"); len(got) != 3 {
		t.Fatalf("separator split = %v, want three phrases", got)
	}
	if got := parseAtomicFeatureResponse("not json at all"); len(got) != 1 || got[0] != "not json at all" {
		t.Fatalf("plain text without separators = %v, want single phrase", got)
	}
	if got := parseAtomicFeatureResponse("```\n[\"one\"]\n```"); len(got) != 1 || got[0] != "one" {
		t.Fatalf("unlabeled fence = %v, want [one]", got)
	}
}

// TestBuildAtomicFeaturePrompt pins the prompt sections fed to the model.
func TestBuildAtomicFeaturePrompt(t *testing.T) {
	prompt := buildAtomicFeatureFeaturePromptTest()
	if !strings.Contains(prompt, "Function: sym") || !strings.Contains(prompt, "Signature: sig") ||
		!strings.Contains(prompt, "Receiver: recv") || !strings.Contains(prompt, "Docstring: doc") {
		t.Fatalf("prompt missing sections: %q", prompt)
	}
	minimal := buildAtomicFeaturePrompt("sym", "", "", "")
	if strings.Contains(minimal, "Signature:") {
		t.Fatalf("minimal prompt must omit empty sections: %q", minimal)
	}
}

func buildAtomicFeatureFeaturePromptTest() string {
	return buildAtomicFeaturePrompt("sym", "sig", "recv", "doc")
}

// TestRPGEncoderAccessors covers the graph/evolver accessors on the encoder.
func TestRPGEncoderAccessors(t *testing.T) {
	root := t.TempDir()
	enc := NewRPGEncoder(NewGOBRPGStore(filepath.Join(root, "rpg.gob")), NewLocalExtractor(), root, RPGEncoderConfig{})
	if enc.GetGraph() == nil {
		t.Fatal("GetGraph must return the graph")
	}
	if enc.GetEvolver() == nil {
		t.Fatal("GetEvolver must return the evolver")
	}
}
