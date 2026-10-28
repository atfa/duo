package xduo

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func webCredentials() (string, string, error) {
	var random [40]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	code := make([]byte, 8)
	for i := range code {
		code[i] = alphabet[random[i]&31]
	}
	return string(code[:4]) + "-" + string(code[4:]), hex.EncodeToString(random[8:]), nil
}

// Only direct loopback access to a loopback hostname is passwordless. A tunnel
// normally preserves its public Host and/or adds forwarding headers; either
// makes it remote even when its TCP connection originates on this machine.
func directLocal(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(peer).IsLoopback() {
		return false
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "CF-Connecting-IP"} {
		if r.Header.Get(header) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}
func sameWebOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && strings.EqualFold(origin.Host, r.Host)
}
func (s *WebServer) authenticated(r *http.Request) bool {
	if directLocal(r) {
		return true
	}
	cookie, err := r.Cookie("xduo_session")
	return err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.sessionKey)) == 1
}
func (s *WebServer) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", 405)
		return
	}
	if !sameWebOrigin(r) {
		http.Error(w, "Invalid origin", 403)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&body) != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(body.Code), "-", ""))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Before(s.loginBlocked) {
		http.Error(w, "Too many attempts. Try again in 30 seconds.", 429)
		return
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(strings.ReplaceAll(s.AccessCode, "-", ""))) != 1 {
		s.loginFailures++
		if s.loginFailures >= 5 {
			s.loginBlocked = now.Add(30 * time.Second)
			s.loginFailures = 0
		}
		http.Error(w, "Incorrect access code", 401)
		return
	}
	s.loginFailures = 0
	http.SetCookie(w, &http.Cookie{Name: "xduo_session", Value: s.sessionKey, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")})
	w.WriteHeader(http.StatusNoContent)
}

func lanWebURLs(port string) []string {
	var result []string
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, a := range addresses {
			ip, _, err := net.ParseCIDR(a.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				result = append(result, "http://"+net.JoinHostPort(ip.String(), port)+"/")
			}
		}
	}
	return result
}
