package xduo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestLocalAndRemoteAccess(t *testing.T) {
	for _, test := range []struct {
		peer, host, header string
		want               bool
	}{
		{"127.0.0.1:1234", "127.0.0.1:8787", "", true},
		{"[::1]:1234", "localhost:8787", "", true},
		{"192.168.1.10:1234", "192.168.1.20:8787", "", false},
		{"127.0.0.1:1234", "desktop.example", "", false},
		{"127.0.0.1:1234", "127.0.0.1:8787", "X-Forwarded-For", false},
		{"127.0.0.1:1234", "127.0.0.1:8787", "CF-Connecting-IP", false},
	} {
		r := httptest.NewRequest("GET", "http://"+test.host+"/", nil)
		r.RemoteAddr = test.peer
		if test.header != "" {
			r.Header.Set(test.header, "192.168.1.10")
		}
		if directLocal(r) != test.want {
			t.Fatalf("local detection: %+v", test)
		}
	}
	server, err := listenWeb(Tmux{}, "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if len(server.AccessCode) != 9 || server.AccessCode[4] != '-' {
		t.Fatal(server.AccessCode)
	}
	remoteRequest := func(method, path, body string) *http.Request {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+strings.TrimPrefix(path, "/"), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "desktop.example"
		r.Header.Set("Origin", "https://desktop.example")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	do := func(r *http.Request) *http.Response {
		t.Helper()
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	res := do(remoteRequest("GET", "/api/session", ""))
	var state map[string]bool
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil || state["authenticated"] {
		t.Fatal(state, err)
	}
	res = do(remoteRequest("POST", "/api/login", `{"code":"WRONG"}`))
	if res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	res = do(remoteRequest("POST", "/api/login", `{"code":"`+strings.ToLower(server.AccessCode)+`"}`))
	if res.StatusCode != 204 {
		data, _ := io.ReadAll(res.Body)
		t.Fatal(res.StatusCode, string(data))
	}
	noHyphen := do(remoteRequest("POST", "/api/login", `{"code":"`+strings.ReplaceAll(server.AccessCode, "-", "")+`"}`))
	if noHyphen.StatusCode != 204 {
		t.Fatal("unformatted access code rejected", noHyphen.StatusCode)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatal(cookies)
	}
	r := remoteRequest("GET", "/api/session", "")
	r.AddCookie(cookies[0])
	res = do(r)
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil || !state["authenticated"] {
		t.Fatal(state, err)
	}
	access, _ := url.Parse(server.URL)
	headers := http.Header{"Host": {"desktop.example"}, "Origin": {"https://desktop.example"}, "X-Forwarded-Proto": {"https"}}
	conn, response, err := websocket.DefaultDialer.Dial("ws://"+access.Host+"/ws", headers)
	if conn != nil {
		conn.Close()
	}
	if err == nil || response.StatusCode != 401 {
		t.Fatal("remote WebSocket did not require login", err)
	}
	response.Body.Close()
	headers.Set("Cookie", cookies[0].String())
	conn, _, err = websocket.DefaultDialer.Dial("ws://"+access.Host+"/ws", headers)
	if err != nil {
		t.Fatal("authenticated tunnel websocket rejected", err)
	}
	conn.Close()
	for i := 0; i < 6; i++ {
		res = do(remoteRequest("POST", "/api/login", `{"code":"WRONG"}`))
	}
	if res.StatusCode != 429 {
		t.Fatal("login attempts not limited", res.StatusCode)
	}
}

func TestStopServerRequiresAuthentication(t *testing.T) {
	server, err := listenWeb(Tmux{}, "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"api/stop", nil)
	req.Host = "remote.example"
	req.Header.Set("Origin", "https://remote.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	select {
	case <-server.StopRequests:
		t.Fatal("unauthenticated stop request")
	default:
	}
	req, _ = http.NewRequest(http.MethodPost, server.URL+"api/stop", nil)
	req.Header.Set("Origin", strings.TrimSuffix(server.URL, "/"))
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal(res.StatusCode)
	}
	select {
	case <-server.StopRequests:
	default:
		t.Fatal("local stop did not reach manager")
	}
}
