package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
)

// withEnv clears the provider variables, then sets the ones a case wants.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{"LLM_PROVIDER", "OPENROUTER_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENCODE_API_KEY", "AUTH_TOKEN", "BASE_URL", "LLM_BASE_URL"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestPickAdapter(t *testing.T) {
	cases := []struct {
		name, wantName, wantKey string
		wantModels              []string
		env                     map[string]string
	}{
		{
			"openrouter key", "openrouter", "or-key",
			[]string{"deepseek/deepseek-v4.1-flash-1017"},
			map[string]string{"OPENROUTER_API_KEY": "or-key"},
		},
		{
			"gemini key", "gemini", "g-key",
			[]string{"gemini-3.6-flash", "gemini-3.5-flash"},
			map[string]string{"GEMINI_API_KEY": "g-key"},
		},
		{
			"google key alias", "gemini", "g-key",
			[]string{"gemini-3.6-flash", "gemini-3.5-flash"},
			map[string]string{"GOOGLE_API_KEY": "g-key"},
		},
		{
			"forced gemini beats an openrouter key", "gemini", "g-key",
			[]string{"gemini-3.6-flash", "gemini-3.5-flash"},
			map[string]string{"OPENROUTER_API_KEY": "or-key", "GEMINI_API_KEY": "g-key", "LLM_PROVIDER": "gemini"},
		},
		{
			"forced openrouter beats a gemini key", "openrouter", "or-key",
			[]string{"deepseek/deepseek-v4.1-flash-1017"},
			map[string]string{"OPENROUTER_API_KEY": "or-key", "GEMINI_API_KEY": "g-key", "LLM_PROVIDER": "openrouter"},
		},
		{"no key plays the mock", "", "", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withEnv(t, c.env)
			got := pickAdapter()
			if got.name != c.wantName || got.key != c.wantKey {
				t.Fatalf("got adapter{name:%q key:%q}, want {%q %q}", got.name, got.key, c.wantName, c.wantKey)
			}
			if c.wantKey == "" && got.keyEnv == "" {
				t.Fatal("a missing key must name the variable that is missing")
			}
			// The default is a model list, so it must survive the same splitting as MODELS.
			if models := splitModels("", got.defaultModel); !slices.Equal(models, c.wantModels) {
				t.Fatalf("models = %v, want %v", models, c.wantModels)
			}
		})
	}
}

func TestCustomAdapterURL(t *testing.T) {
	cases := []struct{ base, want string }{
		{"", "https://ai.naufal.work/v1/chat/completions"},
		{"https://ai.naufal.work", "https://ai.naufal.work/v1/chat/completions"},
		{"https://ai.naufal.work/", "https://ai.naufal.work/v1/chat/completions"},
		{"https://proxy.example/v1", "https://proxy.example/v1/chat/completions"},
		{"https://proxy.example/v1/", "https://proxy.example/v1/chat/completions"},
	}
	for _, c := range cases {
		t.Run("base "+c.base, func(t *testing.T) {
			withEnv(t, map[string]string{"AUTH_TOKEN": "tok", "LLM_PROVIDER": "custom", "BASE_URL": c.base})
			if got := pickAdapter().url; got != c.want {
				t.Fatalf("url = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAdapterRequest(t *testing.T) {
	msgs := []map[string]string{{"role": "user", "content": "hi"}}
	read := func(a adapter, jsonMode bool) (*http.Request, map[string]any) {
		t.Helper()
		req := a.request(context.Background(), "some-model", msgs, jsonMode, false)
		b, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		return req, body
	}

	t.Run("gemini sends no openrouter-only fields", func(t *testing.T) {
		req, body := read(adapter{name: "gemini", url: geminiURL, key: "g-key"}, true)
		if url := req.URL.String(); url != geminiURL {
			t.Errorf("url = %q", url)
		}
		if auth := req.Header.Get("Authorization"); auth != "Bearer g-key" {
			t.Errorf("auth = %q", auth)
		}
		if _, ok := body["provider"]; ok {
			t.Error("sent provider, which gemini rejects")
		}
		if _, ok := body["reasoning"]; ok {
			t.Error("sent reasoning, which gemini rejects")
		}
		if _, ok := body["response_format"]; !ok {
			t.Error("json mode must ask for a JSON object")
		}
	})

	t.Run("opencode go omits temperature and identifies itself", func(t *testing.T) {
		req, body := read(adapter{
			name: "opencode-go", url: opencodeGoURL, key: "oc-key", noTemp: true,
			headers: map[string]string{"User-Agent": "monogatari/1.0", "x-opencode-session": "sess"},
		}, true)
		if url := req.URL.String(); url != opencodeGoURL {
			t.Errorf("url = %q", url)
		}
		if auth := req.Header.Get("Authorization"); auth != "Bearer oc-key" {
			t.Errorf("auth = %q", auth)
		}
		if req.Header.Get("x-opencode-session") == "" {
			t.Error("missing x-opencode-session, which opencode rejects")
		}
		if _, ok := body["temperature"]; ok {
			t.Error("sent temperature, which LongCat 2.5 Free rejects")
		}
		if _, ok := body["response_format"]; !ok {
			t.Error("json mode must ask for a JSON object")
		}
	})

	t.Run("openrouter sends its pin and reasoning", func(t *testing.T) {
		req, body := read(adapter{name: "openrouter", url: openrouterURL, key: "or-key", extra: func(b map[string]any) {
			b["provider"] = map[string]any{"order": []string{"stepfun"}, "allow_fallbacks": false}
			b["reasoning"] = map[string]string{"effort": "low"}
		}}, false)
		if url := req.URL.String(); url != openrouterURL {
			t.Errorf("url = %q", url)
		}
		if auth := req.Header.Get("Authorization"); auth != "Bearer or-key" {
			t.Errorf("auth = %q", auth)
		}
		if _, ok := body["provider"]; !ok {
			t.Error("missing provider pin")
		}
		if _, ok := body["reasoning"]; !ok {
			t.Error("missing reasoning")
		}
		if _, ok := body["response_format"]; ok {
			t.Error("plain mode must not ask for a JSON object")
		}
	})
}
