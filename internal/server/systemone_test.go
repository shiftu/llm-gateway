package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/store"
)

const systemOneRequest = `{"model":"evaluate","state":{"ticket":"refund please","id":9007199254740993},"questions":{"refund":{"type":"noul","instructions":"Refund requested?"},"owner":{"type":"choice","instructions":"Team?","criteria":{"billing":"Billing","other":null}},"urgency":{"type":"score","instructions":"Urgency?","criteria":["normal","urgent","critical"]}}}`
const systemOneResponse = `{"model":"jev-1.13.0","answers":{"refund":{"type":"noul","noul":0.98},"owner":{"type":"choice","choice":"billing","probabilities":{"billing":0.95,"other":0.05},"confidence":0.9},"urgency":{"type":"score","score":1.1,"legend":{"0":"normal","1":"urgent","2":"critical"},"probabilities":{"0":0,"1":0.9,"2":0.1},"confidence":0.82}},"usage":{"input_tokens":296,"output_tokens":20,"cost":0.00003},"id":"gen-example","provider":"TypeSafe"}`

func setupSystemOne(t *testing.T, handler http.Handler) (*store.Store, *Server) {
	t.Helper()
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	st := openTestStore(t)
	mustAdd(t, st, store.Provider{Name: "typesafe", Kind: "typesafe", TypeSafeBaseURL: up.URL + "/v1/", APIKey: "upstream-key"})
	if err := st.SetAlias("evaluate", "typesafe", "jev-latest", nil, nil); err != nil {
		t.Fatal(err)
	}
	return st, NewServer("gw-token", st)
}

func requestSystemOne(s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestSystemOnePassthroughAliasAndUsage(t *testing.T) {
	u := &mockUpstream{respCT: "application/json", respBody: systemOneResponse}
	st, s := setupSystemOne(t, u)
	team, err := st.AddTeam("eval", "Evaluation")
	if err != nil {
		t.Fatal(err)
	}
	ak, key, err := st.IssueAPIKey(team.ID, "inbound", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAliasForTeam("evaluate", team.ID, "typesafe", "jev-1.13.0", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetModelCost(store.ModelCost{Provider: "typesafe", Model: "jev-1.13.0", USDPerInput1k: 1, USDPerOutput1k: 2}); err != nil {
		t.Fatal(err)
	}
	w := requestSystemOne(s, "POST", "/v1/systemone", key, systemOneRequest)
	if w.Code != 200 || w.Body.String() != systemOneResponse {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
	if u.gotURL != "/v1/systemone" || u.gotHeader.Get("Authorization") != "Bearer upstream-key" || u.gotHeader.Get("Accept") != "application/json" {
		t.Fatalf("upstream: %s %v", u.gotURL, u.gotHeader)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(u.gotBody, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["model"]) != `"jev-1.13.0"` || !strings.Contains(string(got["state"]), "9007199254740993") {
		t.Fatalf("alias rewrite damaged state: %s", u.gotBody)
	}
	logs, err := st.TailLogs(1)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs: %v %v", logs, err)
	}
	l, err := st.GetRequestLog(logs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Status != "ok" || l.PromptTokens != 296 || l.CompletionTokens != 20 || l.TotalTokens != 316 || l.TeamID != team.ID || l.APIKeyID != ak.ID {
		t.Fatalf("usage log: %+v", l)
	}
	usage, err := st.GetUsageCounter(ak.ID, truncateToDay(time.Now().UTC()))
	if err != nil || usage.InputTokens != 296 || usage.OutputTokens != 20 || usage.CostUSDMicros != 336000 {
		t.Fatalf("usage: %+v %v", usage, err)
	}
	if err := st.SetQuota(store.Quota{ScopeKind: "key", ScopeID: ak.ID, Window: "day", MaxRequests: new(int64(0))}); err != nil {
		t.Fatal(err)
	}
	if w := requestSystemOne(s, "POST", "/v1/systemone", key, systemOneRequest); w.Code != 429 {
		t.Fatalf("quota bypassed: %d", w.Code)
	}
}

func TestSystemOneRequestFidelityAndStreamFalse(t *testing.T) {
	u := &mockUpstream{respBody: systemOneResponse}
	st, s := setupSystemOne(t, u)
	if err := st.SetDefaultProvider("typesafe"); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(systemOneRequest, `"evaluate"`, `"jev-latest"`, 1)
	w := requestSystemOne(s, "POST", "/v1/systemone", "gw-token", body)
	if w.Code != 200 || string(u.gotBody) != body {
		t.Fatalf("passthrough: %d %s", w.Code, u.gotBody)
	}
	body = strings.TrimSuffix(body, "}") + `,"stream":false}`
	w = requestSystemOne(s, "POST", "/v1/systemone", "gw-token", body)
	if w.Code != 200 || strings.Contains(string(u.gotBody), `"stream"`) {
		t.Fatalf("stream:false was forwarded: %d %s", w.Code, u.gotBody)
	}
}

func TestSystemOneRejectsInvalidRequestsBeforeDispatch(t *testing.T) {
	u := &mockUpstream{respBody: systemOneResponse}
	_, s := setupSystemOne(t, u)
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"model":" "}`, systemOneRequest + `{}`,
		strings.Replace(systemOneRequest, `"state":{`, `"state":null,"unused":{`, 1),
		`{"model":"evaluate","state":42,"questions":{"q":{"type":"noul","instructions":"?"}}}`,
		`{"model":"evaluate","state":"s","questions":{}}`,
		`{"model":"evaluate","state":"s","questions":{"q":{"type":"noul"}}}`,
		`{"model":"evaluate","state":"s","questions":{"q":{"type":"text","instructions":"?"}}}`,
		`{"model":"evaluate","state":"s","questions":{"q":{"type":"choice","instructions":"?","criteria":{}}}}`,
		`{"model":"evaluate","state":"s","questions":{"q":{"type":"score","instructions":"?","criteria":["one"]}}}`,
		`{"model":"evaluate","state":"s","questions":{"q":{"type":"noul","instructions":"?","criteria":{"yes":"yes"}}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			w := requestSystemOne(s, "POST", "/v1/systemone", "gw-token", body)
			if w.Code != 400 {
				t.Fatalf("want 400: %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, field := range []string{`"stream":true`, `"stream":null`, `"stream":"false"`, `"messages":[]`, `"tools":[]`, `"temperature":0`, `"response_format":{}`} {
		w := requestSystemOne(s, "POST", "/v1/systemone", "gw-token", strings.TrimSuffix(systemOneRequest, "}")+","+field+"}")
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d", field, w.Code)
		}
	}
	if u.gotBody != nil {
		t.Fatalf("invalid request reached upstream: %s", u.gotBody)
	}
	if w := requestSystemOne(s, "POST", "/v1/systemone", "", systemOneRequest); w.Code != 401 {
		t.Fatalf("auth: %d", w.Code)
	}
	if w := requestSystemOne(s, "GET", "/v1/systemone", "gw-token", ""); w.Code != 405 {
		t.Fatalf("method: %d", w.Code)
	}
	if w := requestSystemOne(NewServer("gw-token", nil), "POST", "/v1/systemone", "gw-token", systemOneRequest); w.Code != 503 {
		t.Fatalf("stub: %d", w.Code)
	}
}

func TestSystemOneProtocolIsolation(t *testing.T) {
	u := &mockUpstream{respBody: systemOneResponse}
	st, s := setupSystemOne(t, u)
	for path, body := range map[string]string{
		"/v1/chat/completions": `{"model":"evaluate","messages":[{"role":"user","content":"hi"}]}`,
		"/v1/messages":         `{"model":"evaluate","messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":        `{"model":"evaluate","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
	} {
		if w := requestSystemOne(s, "POST", path, "gw-token", body); w.Code != 501 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	mustAdd(t, st, store.Provider{Name: "chat", Kind: "openai", OpenAIBaseURL: "http://127.0.0.1:1", APIKey: "k"})
	if err := st.SetAlias("evaluate", "chat", "chat-model", nil, nil); err != nil {
		t.Fatal(err)
	}
	if w := requestSystemOne(s, "POST", "/v1/systemone", "gw-token", systemOneRequest); w.Code != 501 {
		t.Fatalf("chat target: %d %s", w.Code, w.Body.String())
	}
	if u.gotBody != nil {
		t.Fatal("cross-protocol request reached TypeSafe")
	}
}

func TestSystemOneErrorsAndFallback(t *testing.T) {
	for _, code := range []int{401, 422, 429, 529} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			errorBody := `{"detail":"provider rejected request"}`
			st, s := setupSystemOne(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(errorBody))
			}))
			// An incompatible fallback must leave the original error body open.
			mustAdd(t, st, store.Provider{Name: "chat", Kind: "openai", OpenAIBaseURL: "http://127.0.0.1:1", APIKey: "k"})
			trigger := "http_429"
			if code == 529 {
				trigger = "http_5xx"
			}
			if _, err := st.SetFallbackPolicy(store.FallbackPolicy{Trigger: trigger, Action: "specific_provider", TargetProvider: "chat", MaxChainDepth: 3}); err != nil {
				t.Fatal(err)
			}
			w := requestSystemOne(s, "POST", "/v1/systemone", "gw-token", systemOneRequest)
			if w.Code != code || w.Body.String() != errorBody || w.Header().Get("Retry-After") != "7" {
				t.Fatalf("error lost: %d %s %v", w.Code, w.Body.String(), w.Header())
			}
			logs, _ := st.TailLogs(1)
			if len(logs) != 1 || logs[0].Status != "upstream_error" {
				t.Fatalf("failure log: %+v", logs)
			}
			backup := &mockUpstream{respBody: systemOneResponse}
			up := httptest.NewServer(backup)
			defer up.Close()
			mustAdd(t, st, store.Provider{Name: "backup", Kind: "custom", TypeSafeBaseURL: up.URL + "/api/v1", APIKey: "backup-key"})
			if _, err := st.SetFallbackPolicy(store.FallbackPolicy{Trigger: trigger, Action: "specific_provider", TargetProvider: "backup", MaxChainDepth: 3}); err != nil {
				t.Fatal(err)
			}
			w = requestSystemOne(s, "POST", "/v1/systemone", "gw-token", systemOneRequest)
			if code == 429 || code == 529 {
				if w.Code != 200 || backup.gotURL != "/api/v1/systemone" || backup.gotHeader.Get("Authorization") != "Bearer backup-key" {
					t.Fatalf("fallback: %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != code || backup.gotBody != nil {
				t.Fatal("non-retryable error triggered fallback")
			}
		})
	}
}

func TestSystemOneMalformedSuccessIsBadGateway(t *testing.T) {
	for _, body := range []string{`not json`, `{"model":"jev","answers":{}}`, `{"model":"jev","answers":null,"usage":{"input_tokens":1,"output_tokens":0}}`, `{"model":"jev","answers":{},"usage":{"input_tokens":-1,"output_tokens":0}}`, `{"model":"jev","answers":{},"usage":{"input_tokens":1.5,"output_tokens":0}}`} {
		t.Run(body, func(t *testing.T) {
			st, s := setupSystemOne(t, &mockUpstream{respBody: body})
			w := requestSystemOne(s, "POST", "/v1/systemone", "gw-token", systemOneRequest)
			if w.Code != 502 {
				t.Fatalf("want 502, got %d %s", w.Code, w.Body.String())
			}
			logs, _ := st.TailLogs(1)
			if len(logs) != 1 || logs[0].Status != "upstream_error" || logs[0].TotalTokens != 0 {
				t.Fatalf("logs: %+v", logs)
			}
		})
	}
}
