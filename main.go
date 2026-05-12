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

type PendingRequest struct {
	TokenChan chan map[string]interface{}
	ErrorChan chan string
}

var (
	store           = NewStore()
	tmpls           = template.Must(template.ParseFS(templateFS, "templates/*.html"))
	pendingRequests sync.Map

	googleClientID     = os.Getenv("OAUTH2PLAYGROUND_GOOGLE_CLIENT_ID")
	googleClientSecret = os.Getenv("OAUTH2PLAYGROUND_GOOGLE_CLIENT_SECRET")
	googleRedirectURI  = envOrDefault("OAUTH2PLAYGROUND_GOOGLE_REDIRECT_URI", "http://localhost:8080")
	googleScopes       = envOrDefault("OAUTH2PLAYGROUND_GOOGLE_SCOPES", "openid email profile")
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /start", handleStartScript)

	mux.HandleFunc("GET /api/google/token", handleGoogleToken)
	mux.HandleFunc("POST /api/google/callback", handleGoogleCallback)

	mux.HandleFunc("POST /api/session", handleCreateSession)
	mux.HandleFunc("POST /api/session/{id}/auth-url", handleAuthURL)
	mux.HandleFunc("POST /api/session/{id}/exchange", handleExchange)

	addr := ":8091"
	log.Printf("OAuth2 Playground v%s on %s", version, addr)
	if googleClientID != "" {
		log.Printf("Google provider configured: client_id=%s redirect_uri=%s", googleClientID[:8]+"...", googleRedirectURI)
	}
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
	googleReady := googleClientID != "" && googleClientSecret != ""
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpls.ExecuteTemplate(w, "index.html", map[string]interface{}{
		"ServerURL":      serverURL(r),
		"Version":        version,
		"GoogleReady":    googleReady,
		"GoogleRedirect": googleRedirectURI,
	})
}

// ---------- Start script ----------

func handleStartScript(w http.ResponseWriter, r *http.Request) {
	su := serverURL(r)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Write([]byte(generateStartScript(su)))
}

// ---------- Google token (long-poll) ----------

func handleGoogleToken(w http.ResponseWriter, r *http.Request) {
	if googleClientID == "" || googleClientSecret == "" {
		writeJSON(w, 400, map[string]string{
			"error":   "google provider not configured",
			"message": "Set OAUTH2PLAYGROUND_GOOGLE_CLIENT_ID and OAUTH2PLAYGROUND_GOOGLE_CLIENT_SECRET",
		})
		return
	}

	sess := &Session{
		Name:         "google",
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     googleClientID,
		ClientSecret: googleClientSecret,
		Scopes:       googleScopes,
		RedirectURI:  googleRedirectURI,
	}
	store.Create(sess)

	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", sess.ClientID)
	v.Set("redirect_uri", sess.RedirectURI)
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

	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{})
		flusher.Flush()
	}

	select {
	case token := <-pr.TokenChan:
		json.NewEncoder(w).Encode(token)
	case err := <-pr.ErrorChan:
		json.NewEncoder(w).Encode(map[string]string{"error": err})
	case <-time.After(5 * time.Minute):
		json.NewEncoder(w).Encode(map[string]string{"error": "timeout", "message": "no callback received within 5 minutes"})
	case <-r.Context().Done():
		log.Printf("google token request cancelled for session %s", sess.ID)
	}
}

// ---------- Google callback ----------

func handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}

	code := r.FormValue("code")
	state := r.FormValue("state")

	if code == "" || state == "" {
		writeJSON(w, 400, map[string]string{"error": "missing code or state"})
		return
	}

	sess, ok := store.FindByState(state)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "session not found for given state"})
		return
	}

	if sess.Name != "google" {
		writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("session provider is %q, not google", sess.Name)})
		return
	}

	result := exchangeCode(sess, code)

	if v, ok := pendingRequests.Load(sess.ID); ok {
		pr := v.(*PendingRequest)
		if result.Status == 200 {
			pr.TokenChan <- result.Data
		} else {
			errMsg := "exchange failed"
			if e, ok := result.Data["error_description"]; ok {
				errMsg = e.(string)
			} else if e, ok := result.Data["error"]; ok {
				errMsg = e.(string)
			}
			pr.ErrorChan <- errMsg
		}
	}

	writeJSON(w, 200, map[string]string{"status": "ok", "message": "token sent to pending request"})
}

// ---------- Session API ----------

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

// ---------- Start script generation ----------

func generateStartScript(serverURL string) string {
	googleHint := ""
	if googleClientID != "" && googleClientSecret != "" {
		googleHint = fmt.Sprintf(`echo ""
echo "  Quick start with Google:"
echo "    curl -v %s/api/google/token"
echo "    (then in another terminal after netcat captures the code:)"
echo "    curl -X POST %s/api/google/callback -d \"code=CODE_FROM_NETCAT&state=STATE_FROM_NETCAT\""
echo ""`, serverURL, serverURL)
	}

	s := `#!/bin/bash
# OAuth2 Playground v{{VERSION}} - Interactive flow
# Usage: curl -s {{SERVER}}/start | bash
set -e

SERVER="{{SERVER}}"

echo ""
echo "  >> OAuth2 Playground v{{VERSION}} <<"
echo "  Interactive access token retriever"
echo ""
GOOGLE_HINT

# ---------- config ----------
read -p "  Provider name        [default]: " PROVIDER
PROVIDER=${PROVIDER:-default}

read -p "  Authorization URL    [https://accounts.google.com/o/oauth2/v2/auth]: " AUTH_URL
AUTH_URL=${AUTH_URL:-https://accounts.google.com/o/oauth2/v2/auth}

read -p "  Token URL            [https://oauth2.googleapis.com/token]: " TOKEN_URL
TOKEN_URL=${TOKEN_URL:-https://oauth2.googleapis.com/token}

read -p "  Client ID            : " CLIENT_ID
while [ -z "$CLIENT_ID" ]; do
  read -p "  Client ID (required) : " CLIENT_ID
done

read -s -p "  Client Secret        : " CLIENT_SECRET
echo ""
while [ -z "$CLIENT_SECRET" ]; do
  read -s -p "  Client Secret (req)  : " CLIENT_SECRET
  echo ""
done

read -p "  Scopes               [openid email profile]: " SCOPES
SCOPES=${SCOPES:-openid email profile}

read -p "  Redirect URI         [http://localhost:8080]: " REDIRECT_URI
REDIRECT_URI=${REDIRECT_URI:-http://localhost:8080}

# ---------- create session ----------
echo ""
echo "  Creating session ..."

RESP=$(curl -s -X POST "$SERVER/api/session" \
  -d "name=$PROVIDER" \
  -d "auth_url=$AUTH_URL" \
  -d "token_url=$TOKEN_URL" \
  -d "client_id=$CLIENT_ID" \
  -d "client_secret=$CLIENT_SECRET" \
  -d "scopes=$SCOPES" \
  -d "redirect_uri=$REDIRECT_URI")

SESSION_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)

if [ -z "$SESSION_ID" ]; then
  echo "  ERROR: $RESP"
  exit 1
fi

echo "  Session: $SESSION_ID"

# ---------- get auth url ----------
echo "  Preparing authorization request ..."

AUTH_RESP=$(curl -s -X POST "$SERVER/api/session/$SESSION_ID/auth-url")
AUTH_URL=$(echo "$AUTH_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('auth_url',''))" 2>/dev/null || echo "$AUTH_RESP" | grep -o '"auth_url":"[^"]*"' | cut -d'"' -f4)

if [ -z "$AUTH_URL" ]; then
  echo "  ERROR: $AUTH_RESP"
  exit 1
fi

echo "  Opening browser ..."

if command -v xdg-open &>/dev/null; then
  xdg-open "$AUTH_URL" 2>/dev/null || true
elif command -v open &>/dev/null; then
  open "$AUTH_URL" 2>/dev/null || true
else
  echo ""
  echo "  Open this URL in your browser:"
  echo "  $AUTH_URL"
fi

# ---------- listen ----------
PORT=$(echo "$REDIRECT_URI" | sed 's/.*:\([0-9]*\).*/\1/')
[ -z "$PORT" ] && PORT=8080

echo "  Listening on port $PORT ..."

RESP_HTTP=$(printf "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<!DOCTYPE html><html><body><h1>OK</h1><script>window.close()</script></body></html>")

REQUEST=""
if command -v nc &>/dev/null; then
  REQUEST=$(printf "%s" "$RESP_HTTP" | nc -l "$PORT" 2>/dev/null || true)
  if [ -z "$REQUEST" ]; then
    REQUEST=$(printf "%s" "$RESP_HTTP" | nc -l -p "$PORT" 2>/dev/null || true)
  fi
fi

if [ -z "$REQUEST" ]; then
  echo "  ERROR: no callback received on port $PORT"
  echo "  Make sure netcat (nc) is installed"
  exit 1
fi

echo "  Callback received!"

# ---------- extract code ----------
FIRST=$(echo "$REQUEST" | head -1)

ERR=$(echo "$FIRST" | sed 's/.*[?&]error=\([^& ]*\).*/\1/')
if [ "$ERR" != "$FIRST" ]; then
  DESC=$(echo "$FIRST" | sed 's/.*[?&]error_description=\([^& ]*\).*/\1/')
  echo "  ERROR from provider: $ERR"
  [ -n "$DESC" ] && echo "  Description: $(echo "$DESC" | sed 's/+/ /g')"
  exit 1
fi

CODE=$(echo "$FIRST" | sed 's/.*[?&]code=\([^& ]*\).*/\1/')
STATE=$(echo "$FIRST" | sed 's/.*[?&]state=\([^& ]*\).*/\1/')

if [ -z "$CODE" ] || [ "$CODE" = "$FIRST" ]; then
  echo "  ERROR: could not find authorization code in callback"
  echo "  Raw: $FIRST"
  exit 1
fi

echo "  Authorization code extracted"
echo ""

# ---------- exchange ----------
echo "  Exchanging code for access token ..."

RESULT=$(curl -s -X POST "$SERVER/api/session/$SESSION_ID/exchange" -d "code=$CODE")

echo ""
echo "  ========================"
echo "    ACCESS TOKEN"
echo "  ========================"
if command -v python3 &>/dev/null; then
  echo "$RESULT" | python3 -m json.tool 2>/dev/null || echo "$RESULT"
else
  echo "$RESULT"
fi
echo "  ========================"
echo ""
`

	s = strings.ReplaceAll(s, "{{VERSION}}", version)
	s = strings.ReplaceAll(s, "{{SERVER}}", serverURL)
	s = strings.ReplaceAll(s, "GOOGLE_HINT", googleHint)
	return s
}
