package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func sampleSearch(marker string) []byte {
	return []byte(fmt.Sprintf(`{"model":"test","tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_call","id":"tsc_%s","call_id":"call_%s","arguments":{"query":"%s"},"execution":"client"},{"type":"tool_search_output","call_id":"call_%s","status":"completed","execution":"client","tools":[{"type":"function","name":"read_%s","parameters":{"type":"object","properties":{}}}]}]}`, marker, marker, marker, marker, marker))
}
func TestSIWCToolSearchValidCases(t *testing.T) {
	cases := map[string]string{
		"normal_text":                 `{"input":[{"role":"user","content":"hi"}]}`,
		"ordinary_function":           `{"tools":[{"type":"function","name":"read_file"}],"input":[{"type":"function_call","name":"read_file","call_id":"f1","arguments":"{}"},{"type":"function_call_output","call_id":"f1","output":"ok"}]}`,
		"custom_only":                 `{"tools":[{"type":"custom","name":"patch"}],"input":[{"type":"custom_tool_call","name":"patch","call_id":"c1","input":"*** patch"},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}`,
		"native_namespace":            `{"tools":[{"type":"namespace","name":"repo","tools":[{"type":"function","name":"read"}]}],"input":[]}`,
		"search_declaration_only":     `{"tools":[{"type":"tool_search"}],"input":[]}`,
		"history_without_declaration": `{"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"file"}},{"type":"tool_search_output","call_id":"s1","tools":[]}]}`,
		"empty_explicit_tools":        `{"tools":[],"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"file"}},{"type":"tool_search_output","call_id":"s1","tools":[]}]}`,
		"legacy_object_output":        `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s1","arguments":"{\"query\":\"file\"}"},{"type":"tool_search_output","call_id":"s1","output":{"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}}]}`,
		"legacy_string_output":        `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"file"}},{"type":"tool_search_output","call_id":"s1","output":"no matches"}]}`,
		"failed_discovery":            `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"file"}},{"type":"tool_search_output","call_id":"s1","status":"failed","output":"timeout"}]}`,
		"two_searches":                `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"one"}},{"type":"tool_search_output","call_id":"s1","tools":[]},{"type":"tool_search_call","call_id":"s2","arguments":{"query":"two"}},{"type":"tool_search_output","call_id":"s2","tools":[]}]}`,
		"pending_search":              `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"one"}}]}`,
		"choice":                      `{"tools":[{"type":"tool_search"}],"tool_choice":{"type":"tool_search"},"input":[]}`,
		"unicode":                     `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s1","arguments":{"query":"读取文件 中文🚀"}},{"type":"tool_search_output","call_id":"s1","tools":[]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			out, changed, err := AdaptSIWCToolSearch([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			var d map[string]any
			if json.Unmarshal(out, &d) != nil {
				t.Fatal("bad JSON")
			}
			for _, v := range asList(d["input"]) {
				if m, ok := v.(map[string]any); ok && (m["type"] == "tool_search_call" || m["type"] == "tool_search_output") {
					t.Fatal("unsupported input remained")
				}
			}
			if name == "normal_text" || name == "ordinary_function" || name == "custom_only" || name == "native_namespace" {
				if changed || !bytes.Equal(out, []byte(raw)) {
					t.Fatal("unrelated request changed")
				}
			}
			if name == "choice" {
				if d["tool_choice"].(map[string]any)["type"] != "function" {
					t.Fatal("choice not lowered")
				}
			}
			if name == "legacy_object_output" {
				if len(asList(d["tools"])) != 2 {
					t.Fatal("discovery not promoted")
				}
			}
		})
	}
}
func asList(x any) []any { a, _ := x.([]any); return a }
func TestSIWCToolSearchPreservesNativeItems(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"tool_search"},{"type":"custom","name":"patch","format":{"type":"text"}},{"type":"namespace","name":"repo","tools":[{"type":"function","name":"read"}]}],"input":[{"type":"custom_tool_call","name":"patch","call_id":"c1","input":"text"},{"type":"custom_tool_call_output","call_id":"c1","output":{"type":"tool_search_call","name":"literal user data"}},{"type":"configuration_update","config":{"reasoning":{"effort":"low"}}},{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,placeholder"}]}]}`)
	out, _, err := AdaptSIWCToolSearch(raw)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	json.Unmarshal(raw, &a)
	json.Unmarshal(out, &b)
	ai, _ := json.Marshal(a["input"])
	bi, _ := json.Marshal(b["input"])
	if !bytes.Equal(ai, bi) {
		t.Fatal("native input modified")
	}
	at := asList(a["tools"])
	bt := asList(b["tools"])
	for i := 1; i < len(at); i++ {
		x, _ := json.Marshal(at[i])
		y, _ := json.Marshal(bt[i])
		if !bytes.Equal(x, y) {
			t.Fatal("native tool modified")
		}
	}
}
func TestSIWCToolSearchRejectsUnsafeCases(t *testing.T) {
	cases := map[string]string{
		"missing_call_id":  `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","arguments":{}}]}`,
		"missing_output":   `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s","arguments":{}},{"type":"tool_search_output","call_id":"s"}]}`,
		"bad_arguments":    `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s","arguments":"bad json"}]}`,
		"proxy_collision":  `{"tools":[{"type":"tool_search"},{"type":"function","name":"tool_search"}],"input":[]}`,
		"duplicate_call":   `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s","arguments":{}},{"type":"tool_search_call","call_id":"s","arguments":{}}]}`,
		"duplicate_output": `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"s","arguments":{}},{"type":"tool_search_output","call_id":"s","tools":[]},{"type":"tool_search_output","call_id":"s","tools":[]}]}`,
		"schema_conflict":  `{"tools":[{"type":"tool_search"},{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[{"type":"tool_search_call","call_id":"s","arguments":{}},{"type":"tool_search_output","call_id":"s","tools":[{"type":"function","name":"read","parameters":{"type":"string"}}]}]}`,
	}
	for n, s := range cases {
		t.Run(n, func(t *testing.T) {
			out, _, err := AdaptSIWCToolSearch([]byte(s))
			if err == nil {
				t.Fatal("unsafe input accepted")
			}
			if !bytes.Equal(out, []byte(s)) {
				t.Fatal("error modified original body")
			}
		})
	}
}
func TestSIWCToolSearchConcurrentIsolation(t *testing.T) {
	start := time.Now()
	const workers = 64
	const each = 100
	var wg sync.WaitGroup
	fail := make(chan string, workers)
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				marker := fmt.Sprintf("w%03d_n%03d", n, i)
				original := sampleSearch(marker)
				out, ch, err := AdaptSIWCToolSearch(original)
				if err != nil || !ch || !bytes.Contains(out, []byte("read_"+marker)) {
					fail <- "request " + marker
					return
				}
				r := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{ToolSearch: true})
				for k, event := range []string{
					fmt.Sprintf(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_%s","call_id":"call_%s","name":"tool_search","arguments":""}}`, marker, marker),
					fmt.Sprintf(`{"type":"response.function_call_arguments.delta","sequence_number":1,"output_index":0,"item_id":"fc_%s","delta":"{\"query\":\"%s\"}"}`, marker, marker),
					fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"fc_%s","call_id":"call_%s","name":"tool_search","arguments":"{\"query\":\"%s\"}"}}`, marker, marker, marker),
				} {
					rows, _, e := r.RestoreEvent([]byte(event))
					if e != nil {
						fail <- e.Error()
						return
					}
					if k == 2 {
						if len(rows) != 1 || !bytes.Contains(rows[0], []byte(marker)) || !bytes.Contains(rows[0], []byte("tool_search_call")) {
							fail <- "stream " + marker
							return
						}
					}
				}
			}
		}(n)
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		t.Error(e)
	}
	t.Logf("workers=%d requests=%d elapsed_ms=%d", workers, workers*each, time.Since(start).Milliseconds())
}
func TestSIWCToolSearchNamespaceToolSearchResponse(t *testing.T) {
	payload := []byte(`{"output":[{"type":"function_call","name":"tool_search","namespace":"repo","call_id":"native","arguments":"{}"}]}`)
	out, err := RestoreSIWCToolSearchPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(`"type":"tool_search_call"`)) {
		t.Fatal("namespaced native tool must stay a function")
	}
}
func BenchmarkSIWCToolSearchAdapt(b *testing.B) {
	body := sampleSearch("bench")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, err := AdaptSIWCToolSearch(body)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestSIWCToolSearchPreservesLargeNumbersAndNativeStream(t *testing.T) {
	raw := []byte(`{"metadata":{"number":9007199254740993},"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"a","arguments":{"query":"read"}},{"type":"tool_search_output","call_id":"a","tools":[]}]}`)
	out, _, err := AdaptSIWCToolSearch(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatal("numeric precision changed")
	}
	r := NewSIWCToolSearchRestorer()
	for _, data := range []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_search","call_id":"search","name":"tool_search","arguments":""}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":1,"item":{"type":"function_call","namespace":"repo","id":"fc_native","call_id":"native","name":"tool_search","arguments":""},"vendor_field":"keep"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":1,"item_id":"fc_native","delta":"{}","vendor_field":"keep"}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":1,"item":{"type":"function_call","namespace":"repo","id":"fc_native","call_id":"native","name":"tool_search","arguments":"{}"},"vendor_field":"keep"}`,
	} {
		rows, err := r.RestoreEvent([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains([]byte(data), []byte("vendor_field")) {
			if len(rows) != 1 || !bytes.Equal(rows[0], []byte(data)) {
				t.Fatalf("native stream changed: %s", rows)
			}
		}
	}
	payload := []byte(`{"type":"response.completed","sequence_number":4,"response":{"output":[{"type":"function_call","namespace":"repo","name":"tool_search","arguments":"{}"},{"type":"custom_tool_call","name":"patch","input":"original"},{"type":"function_call","name":"tool_search","arguments":"{}","call_id":"search"}],"usage":{"input_tokens":9007199254740993}},"vendor":"keep"}`)
	rows, err := r.RestoreEvent(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !bytes.Contains(rows[0], []byte("9007199254740993")) || !bytes.Contains(rows[0], []byte(`"namespace":"repo"`)) || !bytes.Contains(rows[0], []byte(`"type":"tool_search_call"`)) {
		t.Fatal("terminal field loss")
	}
}
