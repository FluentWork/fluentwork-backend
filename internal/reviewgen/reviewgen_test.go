package reviewgen

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestParseGeneratedDocument(t *testing.T) {
	doc, err := parseGeneratedDocument("```json\n{\"review\":{\"goal_achievement\":{},\"issues\":[],\"suggestions\":[],\"comparisons\":[{},{},{}]},\"refine\":{\"blocks\":[{\"intent_zh\":\"x\",\"expression_en\":\"y\",\"anchor_user_said\":\"hello\",\"scene_tag\":\"casual\",\"function_tag\":\"ask\"}]}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Review) == 0 || len(doc.Refine) == 0 {
		t.Fatalf("unexpected parsed doc: %+v", doc)
	}
}

func TestArkGeneratorGenerateParsesAndValidates(t *testing.T) {
	gen := ArkGenerator{
		BaseURL:  "https://ark.example.test/api/v3",
		APIKey:   "key",
		Endpoint: "ep-review",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"choices":[{"message":{"role":"assistant","content":"{\"review\":{\"goal_achievement\":{\"met\":true,\"note\":\"ok\"},\"issues\":[{\"type\":\"idiomatic\",\"original_quote\":\"sync up with the team\",\"hint\":\"Prefer touch base.\"}],\"suggestions\":[{\"text\":\"Use touch base with the team.\"}],\"comparisons\":[{\"user\":\"sync up with the team\",\"better\":\"touch base with the team\"},{\"user\":\"I am blocked on the API review\",\"better\":\"I'm blocked waiting on the API review\"},{\"user\":\"finished the login bug\",\"better\":\"fixed the login bug\"}]},\"refine\":{\"blocks\":[{\"intent_zh\":\"同步进度\",\"expression_en\":\"I'll touch base with the team.\",\"anchor_user_said\":\"sync up with the team\",\"scene_tag\":\"standup\",\"function_tag\":\"report\"}]}}"}}],"usage":{"prompt_tokens":111,"completion_tokens":222}}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	result, err := gen.Generate(context.Background(), Request{
		SessionID:  "s1",
		SceneType:  "standup",
		Transcript: "Yesterday I finished the login bug. Today I will sync up with the team. I am blocked on the API review.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Generator != "ark-review-refine-v1" || result.TokensIn != 111 || result.TokensOut != 222 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestArkGeneratorGenerateRejectsInvalidOutput(t *testing.T) {
	gen := ArkGenerator{
		BaseURL:  "https://ark.example.test/api/v3",
		APIKey:   "key",
		Endpoint: "ep-review",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"choices":[{"message":{"role":"assistant","content":"{\"review\":{\"goal_achievement\":{},\"issues\":[{\"type\":\"grammar\",\"original_quote\":\"not present\"}],\"suggestions\":[],\"comparisons\":[{},{},{}]},\"refine\":{\"blocks\":[{\"intent_zh\":\"x\",\"expression_en\":\"y\",\"anchor_user_said\":\"also missing\",\"scene_tag\":\"standup\",\"function_tag\":\"report\"}]}}"}}]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	_, err := gen.Generate(context.Background(), Request{
		SessionID:  "s1",
		SceneType:  "standup",
		Transcript: "hello world",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestArkGeneratorGenerateDisablesThinking(t *testing.T) {
	var sawBody string
	gen := ArkGenerator{
		BaseURL:  "https://ark.example.test/api/v3",
		APIKey:   "key",
		Endpoint: "ep-review",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(req.Body)
				sawBody = string(raw)
				body := `{"choices":[{"message":{"role":"assistant","content":"{\"review\":{\"goal_achievement\":{\"met\":true,\"note\":\"ok\"},\"issues\":[{\"type\":\"idiomatic\",\"original_quote\":\"sync up with the team\",\"hint\":\"Prefer touch base.\"}],\"suggestions\":[{\"text\":\"Use touch base with the team.\"}],\"comparisons\":[{\"user\":\"sync up with the team\",\"better\":\"touch base with the team\"},{\"user\":\"I am blocked on the API review\",\"better\":\"I'm blocked waiting on the API review\"},{\"user\":\"finished the login bug\",\"better\":\"fixed the login bug\"}]},\"refine\":{\"blocks\":[{\"intent_zh\":\"同步进度\",\"expression_en\":\"I'll touch base with the team.\",\"anchor_user_said\":\"sync up with the team\",\"scene_tag\":\"standup\",\"function_tag\":\"report\"}]}}"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	_, err := gen.Generate(context.Background(), Request{
		SessionID:  "s1",
		SceneType:  "standup",
		Transcript: "Yesterday I finished the login bug. Today I will sync up with the team. I am blocked on the API review.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawBody, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("expected thinking disabled in request body, got %s", sawBody)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
