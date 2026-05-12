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
	ClientID     string    `json:"client_id"`
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

type PageData struct {
	Session   *Session
	ServerURL string
	CurlCmd   string
}

type ExchangeResult struct {
	Success    bool                   `json:"success"`
	Data       map[string]interface{} `json:"data,omitempty"`
	Error      string                 `json:"error,omitempty"`
	RawStatus  int                    `json:"-"`
}

var (
	store = NewStore()
	tmpls = template.Must(template.ParseFS(templateFS, "templates/*.html"))
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /session/{id}", handleSessionPage)
	mux.HandleFunc("POST /api/sessions", handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", handleGetSession)
	mux.HandleFunc("POST /api/sessions/{id}/auth-url", handleAuthURL)
	mux.HandleFunc("POST /api/sessions/{id}/exchange", handleExchange)
	mux.HandleFunc("POST /api/{provider}/token", handleNamedExchange)
	mux.HandleFunc("GET /api/sessions/{id}/script", handleScript)

	addr := ":8091"
	log.Printf("OAuth2 Playground v%s on %s", version, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpls.ExecuteTemplate(w, "index.html", nil)
}

func handleSessionPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	su := serverURL(r)
	curlCmd := fmt.Sprintf("curl -s %s/api/sessions/%s/script | bash", su, id)
	data := PageData{Session: sess, ServerURL: su, CurlCmd: curlCmd}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpls.ExecuteTemplate(w, "session.html", data)
}

// ---------- API ----------

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}
	store.Create(sess)
	http.Redirect(w, r, "/session/"+sess.ID, http.StatusSeeOther)
}

func handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sess)
}

func handleAuthURL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		http.NotFound(w, r)
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"auth_url": authURL})
}

func handleExchange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "Missing code", http.StatusBadRequest)
		return
	}
	result := exchangeCode(sess, code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.RawStatus)
	json.NewEncoder(w).Encode(result)
}

func handleNamedExchange(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")
	state := r.FormValue("state")
	if code == "" || state == "" {
		http.Error(w, "Missing code or state", http.StatusBadRequest)
		return
	}
	sess, ok := store.FindByState(state)
	if !ok {
		http.Error(w, "Session not found for given state", http.StatusNotFound)
		return
	}
	if sess.Name != provider {
		http.Error(w, fmt.Sprintf("provider mismatch: expected %q, got %q", sess.Name, provider), http.StatusBadRequest)
		return
	}
	result := exchangeCode(sess, code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.RawStatus)
	json.NewEncoder(w).Encode(result)
}

func handleScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	su := serverURL(r)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Write([]byte(generateScript(sess, su)))
}

// ---------- OAuth2 exchange ----------

func exchangeCode(sess *Session, code string) ExchangeResult {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", sess.RedirectURI)
	v.Set("client_id", sess.ClientID)
	v.Set("client_secret", sess.ClientSecret)

	resp, err := http.PostForm(sess.TokenURL, v)
	if err != nil {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = err.Error() })
		return ExchangeResult{Success: false, Error: err.Error(), RawStatus: 502}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = string(body) })
		return ExchangeResult{Success: false, Error: "invalid response: " + string(body), Data: map[string]interface{}{"raw": string(body)}, RawStatus: resp.StatusCode}
	}

	if accessToken, ok := data["access_token"].(string); ok {
		tokenType, _ := data["token_type"].(string)
		expiresIn, _ := data["expires_in"].(float64)
		store.Update(sess.ID, func(s *Session) {
			s.Status = "completed"
			s.AccessToken = accessToken
			s.TokenType = tokenType
			s.ExpiresIn = int(expiresIn)
			if rt, ok := data["refresh_token"].(string); ok {
				s.RefreshToken = rt
			}
		})
		return ExchangeResult{Success: true, Data: data, RawStatus: 200}
	}

	if errDesc, ok := data["error_description"].(string); ok {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = errDesc })
		return ExchangeResult{Success: false, Error: errDesc, Data: data, RawStatus: resp.StatusCode}
	}
	if errCode, ok := data["error"].(string); ok {
		store.Update(sess.ID, func(s *Session) { s.Status = "error"; s.Error = errCode })
		return ExchangeResult{Success: false, Error: errCode, Data: data, RawStatus: resp.StatusCode}
	}

	return ExchangeResult{Success: false, Error: "unexpected response", Data: data, RawStatus: resp.StatusCode}
}

// ---------- Script generation ----------

func generateScript(sess *Session, serverURL string) string {
	s := `#!/bin/bash
# OAuth2 Playground v` + version + ` - Local callback script
set -e

SESSION_ID="{{SESSION_ID}}"
REDIRECT_URI="{{REDIRECT_URI}}"
SERVER="{{SERVER_URL}}"
PROVIDER="{{PROVIDER}}"

echo "=== OAuth2 Playground ==="
echo "Session: $SESSION_ID  Provider: $PROVIDER"
echo ""

PORT=$(echo "$REDIRECT_URI" | sed 's/.*:\([0-9]*\).*/\1/')
[ -z "$PORT" ] && PORT=8080
echo "Callback port: $PORT"

# ------- get auth url -------
echo "Getting authorization URL ..."
AUTH_URL=$(curl -s -X POST "$SERVER/api/sessions/$SESSION_ID/auth-url" \
    | grep -o '"auth_url":"[^"]*"' | cut -d'"' -f4)
if [ -z "$AUTH_URL" ]; then
    echo "ERROR: failed to get authorization URL"
    exit 1
fi

# ------- open browser -------
echo "Opening browser ..."
if command -v xdg-open &>/dev/null; then
    xdg-open "$AUTH_URL" 2>/dev/null || true
elif command -v open &>/dev/null; then
    open "$AUTH_URL" 2>/dev/null || true
else
    echo "Open this URL in your browser:"
    echo "$AUTH_URL"
fi
echo ""

# ------- listen for callback -------
echo "Waiting for OAuth2 redirect on port $PORT ..."

RESP=$(printf "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<!DOCTYPE html><html><body><h1>OK</h1><script>window.close()</script></body></html>")

REQUEST=""
if command -v nc &>/dev/null; then
    REQUEST=$(printf "%s" "$RESP" | nc -l "$PORT" 2>/dev/null || true)
    if [ -z "$REQUEST" ]; then
        REQUEST=$(printf "%s" "$RESP" | nc -l -p "$PORT" 2>/dev/null || true)
    fi
fi

if [ -z "$REQUEST" ]; then
    echo "ERROR: no callback received on port $PORT"
    echo "Install netcat (nc) or check the port"
    exit 1
fi

echo "Callback received!"

# ------- extract code & state -------
FIRST=$(echo "$REQUEST" | head -1)

ERR=$(echo "$FIRST" | sed 's/.*[?&]error=\([^& ]*\).*/\1/')
if [ "$ERR" != "$FIRST" ]; then
    DESC=$(echo "$FIRST" | sed 's/.*[?&]error_description=\([^& ]*\).*/\1/')
    echo "ERROR from provider: $ERR"
    [ -n "$DESC" ] && echo "Description: $(echo "$DESC" | sed 's/+/ /g')"
    exit 1
fi

CODE=$(echo "$FIRST" | sed 's/.*[?&]code=\([^& ]*\).*/\1/')
STATE=$(echo "$FIRST" | sed 's/.*[?&]state=\([^& ]*\).*/\1/')

if [ -z "$CODE" ] || [ "$CODE" = "$FIRST" ]; then
    echo "ERROR: no authorization code in callback"
    echo "Raw: $FIRST"
    exit 1
fi

echo "Code: ${CODE:0:20}..."
[ -n "$STATE" ] && [ "$STATE" != "$FIRST" ] && echo "State: ${STATE:0:20}..."

# ------- exchange code -------
echo "Exchanging code for token ..."

JSON=$(curl -s -X POST "$SERVER/api/$PROVIDER/token" \
    -d "code=$CODE&state=$STATE" 2>/dev/null || echo "")

if [ -z "$JSON" ]; then
    JSON=$(curl -s -X POST "$SERVER/api/sessions/$SESSION_ID/exchange" \
        -d "code=$CODE")
fi

echo ""
echo "=== Response ==="
if command -v python3 &>/dev/null; then
    echo "$JSON" | python3 -m json.tool 2>/dev/null || echo "$JSON"
else
    echo "$JSON"
fi
echo ""
echo "Done!"
`

	s = strings.ReplaceAll(s, "{{SESSION_ID}}", sess.ID)
	s = strings.ReplaceAll(s, "{{REDIRECT_URI}}", sess.RedirectURI)
	s = strings.ReplaceAll(s, "{{SERVER_URL}}", serverURL)
	s = strings.ReplaceAll(s, "{{PROVIDER}}", sess.Name)
	return s
}
