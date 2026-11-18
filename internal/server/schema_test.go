package server

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// check validates v against the schema subset: type, enum, required, properties, items, minItems.
func check(s map[string]any, v any, path string) error {
	if en, ok := s["enum"].([]any); ok && !slices.Contains(en, v) {
		return fmt.Errorf("%s: %v not in enum %v", path, v, en)
	}
	if t, ok := s["type"]; ok {
		var types []any
		if l, isList := t.([]any); isList {
			types = l
		} else {
			types = []any{t}
		}
		okType := false
		for _, ty := range types {
			switch ty {
			case "object":
				_, k := v.(map[string]any)
				okType = okType || k
			case "array":
				_, k := v.([]any)
				okType = okType || k
			case "string":
				_, k := v.(string)
				okType = okType || k
			case "integer":
				f, k := v.(float64)
				okType = okType || (k && f == math.Trunc(f))
			case "null":
				okType = okType || v == nil
			}
		}
		if !okType {
			return fmt.Errorf("%s: %v has wrong type, want %v", path, v, t)
		}
	}
	if obj, ok := v.(map[string]any); ok {
		for _, r := range asList(s["required"]) {
			if _, has := obj[r.(string)]; !has {
				return fmt.Errorf("%s: missing required key %q", path, r)
			}
		}
		props, _ := s["properties"].(map[string]any)
		for k, sub := range props {
			if x, has := obj[k]; has {
				if err := check(sub.(map[string]any), x, path+"."+k); err != nil {
					return err
				}
			}
		}
	}
	if arr, ok := v.([]any); ok {
		if m, ok := s["minItems"].(float64); ok && float64(len(arr)) < m {
			return fmt.Errorf("%s: fewer than %v items", path, m)
		}
		if it, ok := s["items"].(map[string]any); ok {
			for i, x := range arr {
				if err := check(it, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func asList(v any) []any { l, _ := v.([]any); return l }

func TestO1Schema(t *testing.T) {
	raw, err := os.ReadFile("testdata/chat_completion.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(b string) string {
		if strings.Contains(b, "TOOLS") {
			return "```json\n{\"tool_calls\":[{\"name\":\"f\",\"arguments\":{\"a\":1}}]}\n```"
		}
		return "plain"
	})
	bodies := map[string]string{
		"text": `{"messages":[{"role":"user","content":"a"}]}`,
		"tool": `{"messages":[{"role":"user","content":"a"}],"tools":[{"type":"function","function":{"name":"f"}}]}`,
	}
	for name, req := range bodies {
		st, _, b := e.post(t, "/v1/chat/completions", "r", req)
		var v map[string]any
		if st != 200 || json.Unmarshal(b, &v) != nil {
			t.Fatalf("%s: %d %s", name, st, b)
		}
		if err := check(schema, v, "$"); err != nil {
			t.Errorf("%s: %v\n%s", name, err, b)
		}
		// negative controls: each mutant must fail the same check
		for _, mut := range []struct {
			name string
			f    func(map[string]any)
		}{
			{"drop choices", func(m map[string]any) { delete(m, "choices") }},
			{"bad object", func(m map[string]any) { m["object"] = "chat.completions" }},
			{"created as string", func(m map[string]any) { m["created"] = "now" }},
			{"drop logprobs", func(m map[string]any) { delete(m["choices"].([]any)[0].(map[string]any), "logprobs") }},
			{"bad finish_reason", func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["finish_reason"] = "done" }},
		} {
			var c map[string]any
			_ = json.Unmarshal(b, &c)
			mut.f(c)
			if check(schema, c, "$") == nil {
				t.Errorf("%s: mutant %q passed the schema", name, mut.name)
			}
		}
	}
}
