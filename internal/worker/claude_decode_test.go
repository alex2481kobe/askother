package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clDecode feeds synthetic lines to a decoder built for a new session, or a
// resume of resume when it is set.
func clDecode(t *testing.T, resume string, code int, lines ...string) (Final, []byte, []error) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "answer.tmp")
	in := Invocation{Binary: "c", Mode: "dontAsk", ResumeID: resume, TempAnswer: tmp}
	if _, err := (Claude{}).Build(in); err != nil {
		t.Fatal(err)
	}
	d := Claude{}.NewDecoder(in)
	var errs []error
	for _, l := range lines {
		if _, err := d.Consume([]byte(l)); err != nil {
			errs = append(errs, err)
		}
	}
	f, err := d.Finish(Exit{Code: &code})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(tmp)
	return f, b, errs
}

const (
	clInit   = `{"type":"system","subtype":"init","session_id":"` + clSession + `","permissionMode":"dontAsk"}`
	clGood   = `{"result":"first","is_error":false,"type":"result","subtype":"success","modelUsage":{"a":{"outputTokens":5}}}`
	clBad    = `{"result":"secret error text","is_error":true,"type":"result","subtype":"success","terminal_reason":"api_error"}`
	clLatest = `{"result":"second\n","is_error":false,"type":"result","subtype":"success"}`
)

func TestClaudeDecodeRules(t *testing.T) {
	// The last result decides, and is_error text is never published.
	if f, b, _ := clDecode(t, "", 0, clInit, clGood, clLatest); !f.Success || string(b) != "second\n" || f.SessionID != clSession {
		t.Errorf("good then good: %+v %q", f, b)
	}
	if f, b, _ := clDecode(t, "", 0, clInit, clGood, clBad); f.Success || b != nil || f.Failure.Code != "WORKER_FAILED" {
		t.Errorf("good then error: %+v %q", f, b)
	}
	if f, b, _ := clDecode(t, "", 0, clInit, clBad, clLatest); !f.Success || string(b) != "second\n" {
		t.Errorf("error then good: %+v %q", f, b)
	}
	if f, b, _ := clDecode(t, "", 1, clInit, clGood); f.Success || b != nil {
		t.Errorf("good result, exit 1: %+v %q", f, b)
	}
	// Events and fields Orca does not read never fail a run, whatever their shape.
	if f, _, errs := clDecode(t, "", 0, clInit, `{"type":"rate_limit_event"}`, `{"no_type":1}`, "",
		`{"type":"assistant","message":5,"session_id":7}`, `{"type":"system","subtype":"permission_denied","tool_name":[]}`,
		`{"result":"x","is_error":false,"type":"result","usage":"many","modelUsage":7,"api_error_status":"x"}`); !f.Success || errs != nil {
		t.Errorf("unknown events: %+v %v", f, errs)
	}
	if f, b, _ := clDecode(t, clSession, 0, clInit, clGood); !f.Success || string(b) != "first" || f.SessionID != clSession {
		t.Errorf("resume same: %+v %q", f, b)
	}
	wrong := strings.Replace(clInit, clSession, "00000000-0000-4000-8000-000000000002", 1)
	for name, c := range map[string]struct {
		resume string
		lines  []string
		errs   int
	}{
		"resume mismatch":    {clSession, []string{wrong, clGood}, 1},
		"second session":     {"", []string{clInit, wrong, clGood}, 1},
		"no session":         {"", []string{clGood}, 0},
		"resume, no session": {clSession, []string{clGood}, 0},
		"init without id":    {"", []string{`{"type":"system","subtype":"init"}`, clGood}, 1},
		"malformed":          {"", []string{clInit, `{"type":"assistant"`, clGood}, 1},
		"result lacks text":  {"", []string{clInit, `{"type":"result","is_error":false}`}, 1},
	} {
		f, b, errs := clDecode(t, c.resume, 0, c.lines...)
		if f.Success || b != nil || len(errs) != c.errs || f.Failure.Code != "PROTOCOL" {
			t.Errorf("%s: %+v %q %v", name, f, b, errs)
		}
	}
	long := `{"type":"result","is_error":true,"result":"` + strings.Repeat("猫", 400) + `"}`
	if f, _, _ := clDecode(t, "", 1, clInit, long); !f.Failure.Truncated || len(f.Failure.Message) > 700 {
		t.Errorf("long error: %+v", f.Failure)
	}
}
