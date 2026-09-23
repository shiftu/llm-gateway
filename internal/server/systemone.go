package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (s *Server) dispatchTypeSafe(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		writeStructuredError(w, http.StatusServiceUnavailable, "systemone_unavailable",
			"gateway has no store configured", "configure a store and a provider with typesafe_base_url")
		return
	}
	s.routeAndForward(w, r, protocolTypeSafe)
}

// Validate the documented evaluation shape without decoding opaque state or
// criteria into float64: model alias rewrites must preserve JSON numbers.
func validateSystemOne(body map[string]json.RawMessage) error {
	for field := range body {
		switch field {
		case "model", "state", "questions", "stream":
		default:
			return fmt.Errorf("field %q is not supported by System One", field)
		}
	}
	if raw, ok := body["stream"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
		return fmt.Errorf("System One only supports non-streaming requests; omit stream or set it to false")
	}
	if !systemOneValue(body["state"]) {
		return fmt.Errorf("state must be a string, object or array")
	}
	var questions map[string]json.RawMessage
	if json.Unmarshal(body["questions"], &questions) != nil || len(questions) == 0 {
		return fmt.Errorf("questions must be a non-empty object")
	}
	for name, raw := range questions {
		var q struct {
			Type         string          `json:"type"`
			Instructions json.RawMessage `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
		}
		if json.Unmarshal(raw, &q) != nil || !systemOneValue(q.Instructions) {
			return fmt.Errorf("question %q requires instructions as a string, object or array", name)
		}
		switch q.Type {
		case "noul", "choice":
			if q.Type == "noul" && q.Criteria == nil {
				continue
			}
			var criteria map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || criteria == nil {
				return fmt.Errorf("question %q criteria must be an object", name)
			}
			if q.Type == "choice" && (len(criteria) == 0 || len(criteria) > 255) {
				return fmt.Errorf("question %q choice criteria must have 1 to 255 options", name)
			}
			for key, value := range criteria {
				if q.Type == "noul" && key != "true" && key != "false" {
					return fmt.Errorf("question %q noul criteria only accept true and false keys", name)
				}
				if !systemOneValue(value) && !(q.Type == "choice" && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
					return fmt.Errorf("question %q has invalid criteria value for %q", name, key)
				}
			}
		case "score":
			var criteria []json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) < 2 || len(criteria) > 10 {
				return fmt.Errorf("question %q score criteria must be an array of 2 to 10 levels", name)
			}
			for _, value := range criteria {
				if !systemOneValue(value) {
					return fmt.Errorf("question %q score levels must be strings, objects or arrays", name)
				}
			}
		default:
			return fmt.Errorf("question %q type must be noul, choice or score", name)
		}
	}
	return nil
}

func systemOneValue(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '"' || raw[0] == '{' || raw[0] == '[')
}

// Preserve answers and provider extensions verbatim. Invalid successful
// responses must not be recorded as successful, billable evaluations.
func pipeSystemOneResponse(w http.ResponseWriter, resp *http.Response) (capturedUsage, error) {
	body, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var result struct {
			Model   string                     `json:"model"`
			Answers map[string]json.RawMessage `json:"answers"`
			Usage   struct {
				Input  *int `json:"input_tokens"`
				Output *int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &result) != nil || strings.TrimSpace(result.Model) == "" || result.Answers == nil ||
			result.Usage.Input == nil || result.Usage.Output == nil || *result.Usage.Input < 0 || *result.Usage.Output < 0 {
			err = fmt.Errorf("invalid System One response: expected model, answers and non-negative integer token usage")
		}
	}
	if err != nil {
		writeStructuredError(w, http.StatusBadGateway, "invalid_upstream_response", err.Error(),
			"check the provider's System One endpoint and response")
		return capturedUsage{}, err
	}
	for _, h := range []string{"Retry-After", "retry-after-ms", "X-Request-Id", "Request-Id"} {
		if value := resp.Header.Get(h); value != "" {
			w.Header().Set(h, value)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return capturedUsage{}, nil
	}
	return parseBlockingUsage(body, protocolTypeSafe), nil
}
