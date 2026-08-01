package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// makeWrbLine builds a synthetic wrb.fr response line (>= 200 chars, as the
// parser requires) with the given inner[4] parts JSON. The real wire format is
// nested: [["wrb.fr","id","<inner>",...],...] so arr[0] is an array.
func makeWrbLine(t *testing.T, partsJSON string) string {
	t.Helper()
	inner := `[null,[null,null,null],null,"en",` + partsJSON + `,null,null,"en",null,[1,2,3]]`
	line := `[["wrb.fr","dS5raY5Tk1C4",` + mustJSONString(t, inner) + `,null,null,null,0,1],null,null]`
	for len(line) < 200 {
		line = line[:len(line)-1] + ",null]"
	}
	return line
}

func TestMakeSapisidhashFormat(t *testing.T) {
	h := makeSapisidhash("dummy-sapisid")
	re := regexp.MustCompile(`^SAPISIDHASH \d+_[0-9a-f]{40}$`)
	if !re.MatchString(h) {
		t.Fatalf("bad sapisidhash: %q", h)
	}
}

func TestBuildPayloadStructure(t *testing.T) {
	body := buildPayload("hello", 1, 4, nil, nil)
	values, err := url.ParseQuery(body)
	if err != nil {
		t.Fatal(err)
	}
	freq := values.Get("f.req")
	var outer []any
	if err := json.Unmarshal([]byte(freq), &outer); err != nil {
		t.Fatal(err)
	}
	innerStr, _ := outer[1].(string)
	var inner []any
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
		t.Fatal(err)
	}
	if len(inner) != 102 {
		t.Fatalf("inner should have 102 slots, got %d", len(inner))
	}
	if inner[79].(float64) != 1 {
		t.Fatalf("mode id should be at [79], got %v", inner[79])
	}
	think := inner[17].([]any)
	if think[0].([]any)[0].(float64) != 4 {
		t.Fatalf("think mode should be [[4]], got %v", inner[17])
	}
	if _, ok := inner[59].(string); !ok {
		t.Fatalf("[59] should be a uuid string")
	}
	if inner[7].(float64) != 1 {
		t.Fatalf("[7] should be 1, got %v", inner[7])
	}
}

func TestBuildPayloadWithFileRefs(t *testing.T) {
	body := buildPayload("hello", 1, 4, []string{"/upload/ref"}, nil)
	values, _ := url.ParseQuery(body)
	var outer []any
	_ = json.Unmarshal([]byte(values.Get("f.req")), &outer)
	var inner []any
	_ = json.Unmarshal([]byte(outer[1].(string)), &inner)
	slot := inner[0].([]any)
	if slot[3] == nil {
		t.Fatal("file refs should be present")
	}
	refs := slot[3].([]any)
	if refs[0].([]any)[2] != "/upload/ref" {
		t.Fatalf("unexpected refs: %v", refs)
	}
}

func TestBuildPayloadExtraFields(t *testing.T) {
	body := buildPayload("hello", 3, 4, nil, map[int]any{31: 2, 80: 3})
	values, _ := url.ParseQuery(body)
	var outer []any
	_ = json.Unmarshal([]byte(values.Get("f.req")), &outer)
	var inner []any
	_ = json.Unmarshal([]byte(outer[1].(string)), &inner)
	if inner[31].(float64) != 2 || inner[80].(float64) != 3 {
		t.Fatalf("extra fields not applied: %v %v", inner[31], inner[80])
	}
}

func TestExtractTextsFromLine(t *testing.T) {
	line := makeWrbLine(t, `[[null,["The quick brown fox jumps"]]]`)
	texts := extractTextsFromLine(line)
	if len(texts) != 1 || texts[0] != "The quick brown fox jumps" {
		t.Fatalf("unexpected texts: %v", texts)
	}
}

func TestExtractTextsFromLineShortLine(t *testing.T) {
	if texts := extractTextsFromLine(`["wrb.fr","x","short"]`); len(texts) != 0 {
		t.Fatalf("short lines should yield nothing: %v", texts)
	}
}

func TestExtractResponseText(t *testing.T) {
	line := makeWrbLine(t, `[[null,["Hello"]],[null,["Hello there!"]]]`)
	raw := ")]}'\n\n" + line + "\n"
	text, err := extractResponseText(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello there!" {
		t.Fatalf("expected longest text, got %q", text)
	}
}

func TestExtractResponseTextBardError(t *testing.T) {
	if _, err := extractResponseText(`...BardErrorInfo [20]...`); err == nil {
		t.Fatal("expected BardErrorInfo error")
	}
}

// TestExtractResponseTextBardErrorJSON covers the real wire format Gemini
// returns when it rejects a request: the error code is nested inside a JSON
// payload as "...application.BardErrorInfo",[1152]". The old regex
// (BardErrorInfo\s*\[(\d+)\]) missed this and produced silent content:null.
func TestExtractResponseTextBardErrorJSON(t *testing.T) {
	raw := `)]}'\n\n159\n[["wrb.fr",null,null,null,null,[13,null,[["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",[1152]]]]]]\n60\n`
	if _, err := extractResponseText(raw); err == nil {
		t.Fatal("expected BardErrorInfo error for JSON-wrapped format")
	}
}

// TestExtractResponseTextBardErrorRegex ensures the new regex still matches
// the plain "BardErrorInfo [N]" format too.
func TestExtractResponseTextBardErrorRegex(t *testing.T) {
	for _, raw := range []string{
		`BardErrorInfo [20]`,
		`BardErrorInfo [429]`,
		`BardErrorInfo",[1152]`,
		`"BardErrorInfo",[999]`,
	} {
		if !reBardError.MatchString(raw) {
			t.Fatalf("reBardError should match %q", raw)
		}
	}
	for _, raw := range []string{
		`BardErrorInfo`,
		`BardErrorInfo []`,
		`BardErrorInfo [abc]`,
	} {
		if reBardError.MatchString(raw) {
			t.Fatalf("reBardError should NOT match %q", raw)
		}
	}
}

// TestExtractResponseTextEmptyIsError verifies a 200 response with no
// extractable text does NOT silently return an empty string (which produced
// content:null for clients). extractResponseText itself returns ("", nil)
// when there is simply no text, but generate() turns that into a retryable
// error — this test pins the parser behavior that generate() relies on.
func TestExtractResponseTextNoText(t *testing.T) {
	text, err := extractResponseText(")]}'\n\n159\n[[\"wrb.fr\",null,null,null,null,[13,null,null]]]\n")
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("expected empty text, got %q", text)
	}
}

func TestCleanText(t *testing.T) {
	in := "Before\n```text?code_reference&code_event_index=0\ncontent\n```\nafter http://googleusercontent.com/card_content/123\n"
	out := cleanText(in, true)
	if strings.Contains(out, "code_reference") || strings.Contains(out, "card_content") {
		t.Fatalf("cleanText did not strip artifacts: %q", out)
	}
	if out != "Before\nafter" {
		t.Fatalf("unexpected clean result: %q", out)
	}
}

func TestHandleLineDeltas(t *testing.T) {
	line1 := makeWrbLine(t, `[[null,["Hello wor"]]]`)
	line2 := makeWrbLine(t, `[[null,["Hello world!"]]]`)
	emitted := ""
	var deltas []string
	if err := handleLine(line1, &emitted, func(d string) { deltas = append(deltas, d) }); err != nil {
		t.Fatal(err)
	}
	if err := handleLine(line2, &emitted, func(d string) { deltas = append(deltas, d) }); err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 || deltas[0] != "Hello wor" || deltas[1] != "ld!" {
		t.Fatalf("unexpected deltas: %v (emitted=%q)", deltas, emitted)
	}
}

func TestHandleLineDuplicate(t *testing.T) {
	line := makeWrbLine(t, `[[null,["Same text"]]]`)
	emitted := "Same text"
	var deltas []string
	if err := handleLine(line, &emitted, func(d string) { deltas = append(deltas, d) }); err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 0 {
		t.Fatalf("duplicate text should not emit deltas: %v", deltas)
	}
}
