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
	Provider     string    `json:"provider"`
	AuthURL      string    `json:"-"`
	TokenURL     string    `json:"-"`
	ClientID     string    `json:"-"`
	ClientSecret string    `json:"-"`
	Scopes       string    `json:"scopes"`
	RedirectURI  string    `json:"redirect_uri"`
	State        string    `json:"state"`
	AuthHash     string    `json:"-"`
	Status       string    `json:"status"`
	AccessToken  string    `json:"access_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type Store struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	byState    map[string]string
	byAuthHash map[string]string
}

func NewStore() *Store {
	return &Store{
		sessions:   make(map[string]*Session),
		byState:    make(map[string]string),
		byAuthHash: make(map[string]string),
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
	if sess.AuthHash != "" {
		s.byAuthHash[sess.AuthHash] = id
	}
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

func (s *Store) FindByAuthHash(hash string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byAuthHash[hash]
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
	oldHash := v.AuthHash
	fn(v)
	if v.AuthHash != oldHash {
		delete(s.byAuthHash, oldHash)
		if v.AuthHash != "" {
			s.byAuthHash[v.AuthHash] = id
		}
	}
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

type ServiceDef struct {
	Name     string // "google", "gmail", "gdrive", "microsoft"
	Provider string // "google" or "microsoft"
	Scopes   string
}

var (
	store           = NewStore()
	tmpls           = template.Must(template.ParseFS(templateFS, "templates/*.html"))
	pendingRequests sync.Map
	services        []ServiceDef
)

func loadServices() []ServiceDef {
	var svcs []ServiceDef

	if os.Getenv("GOOGLE_CLIENT_ID") != "" {
		svcs = append(svcs,
			ServiceDef{Name: "google", Provider: "google", Scopes: envOr("GOOGLE_SCOPES", "openid email profile")},
			ServiceDef{Name: "gmail", Provider: "google", Scopes: "https://www.googleapis.com/auth/gmail.readonly"},
			ServiceDef{Name: "gdrive", Provider: "google", Scopes: "https://www.googleapis.com/auth/drive.readonly"},
		)
	}

	if os.Getenv("MICROSOFT_CLIENT_ID") != "" {
		svcs = append(svcs,
			ServiceDef{Name: "microsoft", Provider: "microsoft", Scopes: envOr("MICROSOFT_SCOPES", "openid email profile offline_access")},
		)
	}

	return svcs
}

var providerAuthURLs = map[string]string{
	"google":    "https://accounts.google.com/o/oauth2/v2/auth",
	"microsoft": "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
}

var providerTokenURLs = map[string]string{
	"google":    "https://oauth2.googleapis.com/token",
	"microsoft": "https://login.microsoftonline.com/common/oauth2/v2.0/token",
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	services = loadServices()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /auth/{hash}", handleAuthRedirect)
	mux.HandleFunc("GET /api/{service}/token", handleServiceToken)
	mux.HandleFunc("GET /api/{provider}/callback", handleCallback)

	addr := ":8091"
	log.Printf("OAuth2 Playground v%s on %s", version, addr)
	for _, svc := range services {
		log.Printf("  service %s (provider=%s scopes=%s)", svc.Name, svc.Provider, shorten(svc.Scopes, 40))
	}
	if len(services) == 0 {
		log.Printf("  no services configured — set GOOGLE_CLIENT_ID or MICROSOFT_CLIENT_ID")
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
	type SInfo struct {
		Name    string
		Cmd     string
		CmdLong string
	}
	var list []SInfo
	for _, svc := range services {
		cmd := fmt.Sprintf("curl -i %s/api/%s/token > token.txt", serverURL(r), svc.Name)
		cmdLong := fmt.Sprintf("curl -i '%s/api/%s/token?scopes=extra1,extra2' > token.txt", serverURL(r), svc.Name)
		list = append(list, SInfo{Name: svc.Name, Cmd: cmd, CmdLong: cmdLong})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpls.ExecuteTemplate(w, "index.html", map[string]interface{}{
		"ServerURL": serverURL(r),
		"Version":   version,
		"Services":  list,
	})
}

// ---------- Auth redirect (masked URL) ----------

func handleAuthRedirect(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	sess, ok := store.FindByAuthHash(hash)
	if !ok {
		http.NotFound(w, r)
		return
	}

	store.Update(sess.ID, func(s *Session) { s.Status = "started" })

	redirectURI := sess.RedirectURI

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

	http.Redirect(w, r, authURL, http.StatusFound)
}

// ---------- Service token (long-poll) ----------

func handleServiceToken(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("service")

	var svc *ServiceDef
	for i := range services {
		if services[i].Name == name {
			svc = &services[i]
			break
		}
	}
	if svc == nil {
		writeJSON(w, 404, map[string]string{"error": fmt.Sprintf("service %q not configured", name)})
		return
	}

	var clientID, clientSecret, redirectURI string
	switch svc.Provider {
	case "google":
		clientID = os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
		redirectURI = envOr("GOOGLE_REDIRECT_URI", fmt.Sprintf("http://%s/api/google/callback", r.Host))
	case "microsoft":
		clientID = os.Getenv("MICROSOFT_CLIENT_ID")
		clientSecret = os.Getenv("MICROSOFT_CLIENT_SECRET")
		redirectURI = envOr("MICROSOFT_REDIRECT_URI", fmt.Sprintf("http://%s/api/microsoft/callback", r.Host))
	}

	extraScopes := r.URL.Query().Get("scopes")
	scopes := svc.Scopes
	if extraScopes != "" {
		scopes = svc.Scopes + " " + strings.ReplaceAll(extraScopes, ",", " ")
	}

	authURLStr := providerAuthURLs[svc.Provider]
	tokenURLStr := providerTokenURLs[svc.Provider]

	sess := &Session{
		Name:         svc.Name,
		Provider:     svc.Provider,
		AuthURL:      authURLStr,
		TokenURL:     tokenURLStr,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		RedirectURI:  redirectURI,
		AuthHash:     genID(),
	}
	store.Create(sess)

	maskedURL := fmt.Sprintf("http://%s/auth/%s", r.Host, sess.AuthHash)
	store.Update(sess.ID, func(s *Session) { s.Status = "masked" })

	pr := &PendingRequest{
		TokenChan: make(chan map[string]interface{}, 1),
		ErrorChan: make(chan string, 1),
	}
	pendingRequests.Store(sess.ID, pr)
	defer pendingRequests.Delete(sess.ID)

	w.Header().Set("open-on-browser", maskedURL)
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

// ---------- Callback ----------

func handleCallback(w http.ResponseWriter, r *http.Request) {
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

	if sess.Provider != provider {
		renderCallbackHTML(w, "Provider mismatch", fmt.Sprintf("session provider is %q, not %q", sess.Provider, provider))
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

// ---------- Helpers ----------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

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
