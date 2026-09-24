package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestInitializeVersions(t *testing.T) {
	for asked, want := range map[string]string{"2025-11-25": "2025-11-25", "2025-06-18": "2025-06-18", "2024-11-05": "2025-11-25", "": "2025-11-25"} {
		x := newHarness(t)
		m := x.initialize(asked)
		if m["protocolVersion"] != want {
			t.Errorf("asked %q: got %v, want %s", asked, m["protocolVersion"], want)
		}
		if info := fmt.Sprint(m["serverInfo"]); info != "map[name:askother version:0.0.0-test]" {
			t.Errorf("serverInfo %s", info)
		}
		if caps := fmt.Sprint(m["capabilities"]); caps != "map[tools:map[]]" {
			t.Errorf("capabilities %s", caps)
		}
	}
}

func TestRequestIDsEchoedExactly(t *testing.T) {
	x := newHarness(t)
	for _, id := range []string{`7`, `"abc"`, `"7"`, `-3`, `9007199254740993`} {
		x.send(`{"jsonrpc":"2.0","id":%s,"method":"ping"}`, id)
		if r := x.recv(); string(r.ID) != id || r.Error != nil || string(r.Result) != "{}" {
			t.Errorf("id %s: got %s %+v %s", id, r.ID, r.Error, r.Result)
		}
	}
	x.send(`{"jsonrpc":"2.0","id":{},"method":"ping"}`)
	if r := x.recv(); string(r.ID) != "null" || r.Error == nil || r.Error.Code != errInvalidRequest {
		t.Errorf("object id: %s %+v", r.ID, r.Error)
	}
}

func TestSlowCallDoesNotBlockOthers(t *testing.T) {
	x := newHarness(t)
	x.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`)
	x.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if r := x.recv(); string(r.ID) != "2" {
		t.Fatalf("want ping reply first, got id %s", r.ID)
	}
	x.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo"}}`)
	if r := x.recv(); string(r.ID) != "3" {
		t.Fatalf("want echo reply, got id %s", r.ID)
	}
	close(x.h.release)
	if r := x.recv(); string(r.ID) != "1" || !strings.Contains(string(r.Result), "slept") {
		t.Fatalf("slow reply: %s %s", r.ID, r.Result)
	}
}

func TestPanicCostsOneReply(t *testing.T) {
	x := newHarness(t)
	x.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"panic"}}`)
	if r := x.recv(); r.Error == nil || r.Error.Code != errInternal || string(r.ID) != "1" {
		t.Fatalf("panic reply: %s %+v", r.ID, r.Error)
	}
	x.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if r := x.recv(); string(r.ID) != "2" || r.Error != nil {
		t.Fatalf("server stopped serving after panic")
	}
	if !strings.Contains(x.log.String(), "boom") {
		t.Errorf("panic not logged: %q", x.log.String())
	}
}

func TestCancelledNotificationCancelsContext(t *testing.T) {
	x := newHarness(t)
	x.send(`{"jsonrpc":"2.0","id":"c1","method":"tools/call","params":{"name":"slow"}}`)
	time.Sleep(50 * time.Millisecond) // let the call start
	x.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"c1","reason":"timeout"}}`)
	select {
	case <-x.h.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler ctx not cancelled")
	}
	x.expectSilence(200 * time.Millisecond) // no reply to a cancelled request
}

func TestEOFShutdownWithInFlightCall(t *testing.T) {
	x := newHarness(t)
	x.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`)
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	x.in.Close()
	if r := x.recv(); string(r.ID) != "1" || !strings.Contains(string(r.Result), "cancelled") {
		t.Fatalf("in-flight reply: %s %s", r.ID, r.Result)
	}
	select {
	case err := <-x.done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
		x.done <- nil
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after EOF")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("shutdown took %s", d)
	}
}

func TestShutdownAbandonsStubbornHandler(t *testing.T) {
	x := newHarness(t)
	x.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stubborn"}}`)
	time.Sleep(50 * time.Millisecond)
	x.cancel()
	select {
	case err := <-x.done:
		x.done <- err
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("Serve waited past the grace period")
	}
	close(x.h.release)
	x.expectSilence(200 * time.Millisecond) // nothing written after Serve returns
}

func TestNotificationsGetNoReply(t *testing.T) {
	x := newHarness(t)
	x.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	x.send(`{"jsonrpc":"2.0","method":"notifications/unknown","params":{}}`)
	x.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`)
	x.send(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo"}}`)
	x.send(`{"jsonrpc":"2.0","id":5,"method":"ping"}`)
	if r := x.recv(); string(r.ID) != "5" {
		t.Fatalf("first reply is not the ping: id %s", r.ID)
	}
	x.expectSilence(100 * time.Millisecond)
}

type callRes struct {
	Content []textContent   `json:"content"`
	Struct  json.RawMessage `json:"structuredContent"`
	IsError bool            `json:"isError"`
}

func TestToolsCallShapesAndErrors(t *testing.T) {
	x := newHarness(t)
	x.initialize("2025-06-18")

	x.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fail","arguments":{}}}`)
	r := x.recv()
	var cr callRes
	json.Unmarshal(r.Result, &cr)
	if r.Error != nil || !cr.IsError || cr.Content[0].Text != `{"code":"NOT_FOUND","message":"no run \"r1\""}` || string(cr.Struct) != cr.Content[0].Text {
		t.Fatalf("tool error: %s %+v", r.Result, r.Error)
	}

	x.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"a":1},"_meta":{"threadId":"t-1"}}}`)
	r = x.recv()
	cr = callRes{}
	json.Unmarshal(r.Result, &cr)
	want := `{"args":{"a":1},"client":"codex-mcp-client","meta":{"threadId":"t-1"}}`
	if cr.IsError || cr.Content[0].Type != "text" || cr.Content[0].Text != want || string(cr.Struct) != want {
		t.Fatalf("echo: %s", r.Result)
	}

	x.send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if r := x.recv(); !strings.Contains(string(r.Result), `"name":"stubborn"`) {
		t.Fatalf("tools/list: %s", r.Result)
	}

	for id, req := range map[int]string{
		4: `"method":"tools/call","params":{"name":"nope"}`,
		5: `"method":"tools/call","params":"x"`,
		6: `"method":"tools/call","params":{}`,
	} {
		x.send(`{"jsonrpc":"2.0","id":%d,%s}`, id, req)
		if r := x.recv(); r.Error == nil || r.Error.Code != errInvalidParams {
			t.Errorf("id %d: want -32602, got %s %+v", id, r.Result, r.Error)
		}
	}
	x.send(`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)
	if r := x.recv(); r.Error == nil || r.Error.Code != errMethodNotFound {
		t.Errorf("unknown method: %+v", r.Error)
	}
	x.send(`{not json`)
	if r := x.recv(); r.Error == nil || r.Error.Code != errParse || string(r.ID) != "null" {
		t.Errorf("parse error: %+v", r.Error)
	}
}

func TestConcurrentRepliesAreWholeLines(t *testing.T) {
	x := newHarness(t)
	const n = 200
	go func() {
		for i := range n {
			x.send(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"echo","arguments":{"pad":%q}}}`, i, strings.Repeat("x", i*37))
		}
	}()
	seen := map[string]bool{}
	for range n {
		r := x.recv() // fails on any line that is not one whole JSON message
		if r.Error != nil || seen[string(r.ID)] {
			t.Fatalf("bad reply id %s %+v", r.ID, r.Error)
		}
		seen[string(r.ID)] = true
	}
}
