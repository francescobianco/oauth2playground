package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

const version = "0.1.0"

type Session struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	Name         string    `json:"name"`
	AuthURL      string    `json:"auth_url"`
	TokenURL     string    `json:"token_url"`
	ClientID     string    `json:"-"`
	ClientSecret string    `json:"-"`
	Scopes       string    `json:"scopes"`
	RedirectURI  string    `json:"redirect_uri"`
	State        string    `json:"state"`
	Status       string    `json:"status"`
	AccessToken  string    `json:"access_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	byState  map[string]string
}

func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*Session),
		byState:  make(map[string]string),
	}
}

func (s *Store) Create(sess *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := genID()
	sess.ID = id
	sess.CreatedAt = time.Now()
	sess.State = genState()
	sess.Status = "new"
	s.sessions[id] = sess
	s.byState[sess.State] = id
	return id
}

func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.sessions[id]
	return v, ok
}

func (s *Store) FindByState(state string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byState[state]
	if !ok {
		return nil, false
	}
	v, ok := s.sessions[id]
	return v, ok
}

func (s *Store) Update(id string, fn func(*Session)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	if !ok {
		return false
	}
	fn(v)
	return true
}

func genID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func genState() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type PendingRequest struct {
	TokenChan chan map[string]interface{}
	ErrorChan chan string
}

type ProviderConfig struct {
	Name        string
	AuthURL     string
	TokenURL    string
	ClientID    string
	ClientSecret string
	RedirectURI string
	Scopes      string
}

var (
	store           = NewStore()
	tmpls           = template.Must(template.ParseFS(templateFS, "templates/*.html"))
	pendingRequests sync.Map
	providers       map[string]*ProviderConfig
)

func loadProviders() map[string]*ProviderConfig {
	m := make(map[string]*ProviderConfig)

	if id := os.Getenv("GOOGLE_CLIENT_ID"); id != "" {
		m["google"] = &ProviderConfig{
			Name:         "google",
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			ClientID:     id,
			ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
			RedirectURI:  envOr("GOOGLE_REDIRECT_URI", "http://localhost:8091/api/google/callback"),
			Scopes:       envOr("GOOGLE_SCOPES", "openid email profile"),
		}
	}

	if id := os.Getenv("MICROSOFT_CLIENT_ID"); id != "" {
		m["microsoft"] = &ProviderConfig{
			Name:         "microsoft",
			AuthURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			ClientID:     id,
			ClientSecret: os.Getenv("MICROSOFT_CLIENT_SECRET"),
			RedirectURI:  envOr("MICROSOFT_REDIRECT_URI", "http://localhost:8091/api/microsoft/callback"),
			Scopes:       envOr("MICROSOFT_SCOPES", "openid email profile offline_access"),
		}
	}

	return m
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	providers = loadProviders()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /api/{provider}/token", handleProviderToken)
	mux.HandleFunc("GET /api/{provider}/callback", handleProviderCallback)

	addr := ":8091"
	log.Printf("OAuth2 Playground v%s on %s", version, addr)
	for name, p := range providers {
		log.Printf("  provider %s: client_id=%s redirect=%s", name, shorten(p.ClientID, 12), p.RedirectURI)
	}
	if len(providers) == 0 {
		log.Printf("  no providers configured — set GOOGLE_CLIENT_ID or MICROSOFT_CLIENT_ID env vars")
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

// ---------- Web UI ----------

func handleIndex(w http.ResponseWriter, r *http.Request) {
	type PInfo struct {
		Name string
		Cmd  string
	}
	var list []PInfo
	for name := range providers {
		list = append(list, PInfo{
			Name: name,
			Cmd:  fmt.Sprintf("curl -i %s/api/%s/token > token.txt", serverURL(r), name),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpls.ExecuteTemplate(w, "index.html", map[string]interface{}{
		"ServerURL": serverURL(r),
		"Version":   version,
		"Providers": list,
	})
}

// ---------- Token (long-poll) ----------

func handleProviderToken(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	cfg, ok := providers[provider]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": fmt.Sprintf("provider %q not configured", provider)})
		return
	}

	redirectURI := cfg.RedirectURI

	sess := &Session{
		Name:         provider,
		AuthURL:      cfg.AuthURL,
		TokenURL:     cfg.TokenURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Scopes:       cfg.Scopes,
		RedirectURI:  redirectURI,
	}
	store.Create(sess)

	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", sess.ClientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("state", sess.State)
	v.Set("access_type", "offline")
	v.Set("prompt", "consent")
	if sess.Scopes != "" {
		v.Set("scope", strings.ReplaceAll(sess.Scopes, ",", " "))
	}
	authURL := sess.AuthURL
	if strings.Contains(authURL, "?") {
		authURL += "&" + v.Encode()
	} else {
		authURL += "?" + v.Encode()
	}

	store.Update(sess.ID, func(s *Session) { s.Status = "started" })

	pr := &PendingRequest{
		TokenChan: make(chan map[string]interface{}, 1),
		ErrorChan: make(chan string, 1),
	}
	pendingRequests.Store(sess.ID, pr)
	defer pendingRequests.Delete(sess.ID)

	w.Header().Set("open-on-browser", authURL)
	w.Header().Set("X-Session-Id", sess.ID)
	w.Header().Set("Content-Type", "application/json")

	if f, ok := w.(http.Flusher); ok {
		w.WriteHeader(http.StatusOK)
		f.Flush()
	}

	select {
	case token := <-pr.TokenChan:
		json.NewEncoder(w).Encode(token)
	case err := <-pr.ErrorChan:
		json.NewEncoder(w).Encode(map[string]string{"error": err})
	case <-time.After(5 * time.Minute):
		json.NewEncoder(w).Encode(map[string]string{"error": "timeout"})
	case <-r.Context().Done():
	}
}

// ---------- Callback (riceve il redirect dal provider) ----------

func handleProviderCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errStr := r.URL.Query().Get("error")

	if errStr != "" {
		desc := r.URL.Query().Get("error_description")
		renderCallbackHTML(w, fmt.Sprintf("Error: %s", errStr), desc)
		return
	}

	if code == "" || state == "" {
		renderCallbackHTML(w, "Missing parameters", "code and state are required")
		return
	}

	sess, ok := store.FindByState(state)
	if !ok {
		renderCallbackHTML(w, "Session not found", "The state parameter did not match any active session")
		return
	}

	if sess.Name != provider {
		renderCallbackHTML(w, "Provider mismatch", fmt.Sprintf("session is for %q, not %q", sess.Name, provider))
		return
	}

	result := exchangeCode(sess, code)

	if v, ok := pendingRequests.Load(sess.ID); ok {
		pr := v.(*PendingRequest)
		if result.Status == 200 {
			pr.TokenChan <- result.Data
		} else {
			errMsg := "token exchange failed"
			if e, ok := result.Data["error_description"]; ok {
				errMsg = e.(string)
			} else if e, ok := result.Data["error"]; ok {
				errMsg = e.(string)
			}
			pr.ErrorChan <- errMsg
		}
	}

	renderCallbackHTML(w, "Authorization complete!", "You can close this window and check your terminal for the token.")
}

func renderCallbackHTML(w http.ResponseWriter, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body style="font-family:sans-serif;text-align:center;padding:80px 20px">
<h1>%s</h1><p>%s</p><script>window.close()</script></body></html>`, title, msg)
}

// ---------- Session API generica ----------

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	redir := r.FormValue("redirect_uri")
	if redir == "" {
		redir = "http://localhost:8080"
	}
	sess := &Session{
		Name:         r.FormValue("name"),
		AuthURL:      r.FormValue("auth_url"),
		TokenURL:     r.FormValue("token_url"),
		ClientID:     r.FormValue("client_id"),
		ClientSecret: r.FormValue("client_secret"),
		Scopes:       r.FormValue("scopes"),
		RedirectURI:  redir,
	}
	if sess.AuthURL == "" || sess.TokenURL == "" || sess.ClientID == "" || sess.ClientSecret == "" {
		writeJSON(w, 400, map[string]string{"error": "missing required fields"})
		return
	}
	store.Create(sess)
	writeJSON(w, 201, map[string]interface{}{
		"id":           sess.ID,
		"state":        sess.State,
		"name":         sess.Name,
		"redirect_uri": sess.RedirectURI,
		"created_at":   sess.CreatedAt,
	})
}

func handleAuthURL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "session not found"})
		return
	}
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", sess.ClientID)
	v.Set("redirect_uri", sess.RedirectURI)
	v.Set("state", sess.State)
	if sess.Scopes != "" {
		v.Set("scope", strings.ReplaceAll(sess.Scopes, ",", " "))
	}
	authURL := sess.AuthURL
	if strings.Contains(authURL, "?") {
		authURL += "&" + v.Encode()
	} else {
		authURL += "?" + v.Encode()
	}
	store.Update(id, func(s *Session) { s.Status = "started" })
	writeJSON(w, 200, map[string]string{"auth_url": authURL})
}

func handleExchange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "session not found"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	code := r.FormValue("code")
	if code == "" {
		writeJSON(w, 400, map[string]string{"error": "missing code"})
		return
	}
	result := exchangeCode(sess, code)
	writeJSON(w, result.Status, result.Data)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// ---------- OAuth2 exchange ----------

type exchangeResult struct {
	Status int
	Data   map[string]interface{}
}

func exchangeCode(sess *Session, code string) exchangeResult {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", sess.RedirectURI)
	v.Set("client_id", sess.ClientID)
	v.Set("client_secret", sess.ClientSecret)

	resp, err := http.PostForm(sess.TokenURL, v)
	if err != nil {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = err.Error() })
		return exchangeResult{502, map[string]interface{}{"error": err.Error()}}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = string(body) })
		return exchangeResult{502, map[string]interface{}{"error": "invalid response", "raw": string(body)}}
	}

	if token, ok := data["access_token"].(string); ok {
		tokenType, _ := data["token_type"].(string)
		expiresIn, _ := data["expires_in"].(float64)
		store.Update(sess.ID, func(s *Session) {
			s.Status = "completed"
			s.AccessToken = token
			s.TokenType = tokenType
			s.ExpiresIn = int(expiresIn)
			if rt, ok := data["refresh_token"].(string); ok {
				s.RefreshToken = rt
			}
		})
		return exchangeResult{200, data}
	}

	if desc, ok := data["error_description"].(string); ok {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = desc })
		return exchangeResult{400, data}
	}
	if ec, ok := data["error"].(string); ok {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = ec })
		return exchangeResult{400, data}
	}

	return exchangeResult{200, data}
}
