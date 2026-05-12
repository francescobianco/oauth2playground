# OAuth2 Playground

A self-hosted service that lets you obtain OAuth2 access tokens directly from your terminal. No web UI to fill in — just `curl`, authorize in the browser, and the token arrives on the connection.

## How it works

```
curl -v http://localhost:8091/api/google/token > token.json
```

1. Run the command in your terminal
2. Open the URL shown in the response header `visit-auth-URL`
3. Authorize the application in your browser
4. The connection stays open — once the authorization completes server-side, the token arrives in your terminal

## Quick start

### 1. Configure providers

Copy `.env.example` to `.env` and fill in your OAuth2 credentials:

```env
GOOGLE_CLIENT_ID=your-google-client-id
GOOGLE_CLIENT_SECRET=your-google-client-secret
GOOGLE_REDIRECT_URI=http://localhost:8091/api/google/callback
GOOGLE_SCOPES=openid email profile

MICROSOFT_CLIENT_ID=your-microsoft-client-id
MICROSOFT_CLIENT_SECRET=your-microsoft-client-secret
MICROSOFT_REDIRECT_URI=http://localhost:8091/api/microsoft/callback
MICROSOFT_SCOPES=openid email profile offline_access
```

> **Important**: The `GOOGLE_CLIENT_ID` enables three services: `google`, `gmail`, and `gdrive` — each with their own default scopes.

### 2. Start the server

```bash
docker compose up --build
```

Or without Docker:

```bash
go build -o playground . && ./playground
```

The server listens on port `8091`.

### 3. Get a token

```bash
curl -v http://localhost:8091/api/gmail/token > token.json
```

Open the `visit-auth-URL` shown in the response headers, authorize, and the token lands in `token.json`.

## Available services

| Service    | Provider   | Default scopes |
|------------|------------|----------------|
| `google`   | Google     | `openid email profile` |
| `gmail`    | Google     | `https://www.googleapis.com/auth/gmail.readonly` |
| `gdrive`   | Google     | `https://www.googleapis.com/auth/drive.readonly` |
| `microsoft`| Microsoft  | `openid email profile offline_access` |

Each service shares the same `CLIENT_ID` / `CLIENT_SECRET` of its parent provider but uses different default scopes.

## Query parameters

| Parameter | Description | Example |
|-----------|-------------|---------|
| `scopes`  | Additional scopes (comma-separated) | `?scopes=https://www.googleapis.com/auth/gmail.labels` |
| `format`  | Output format: `json` (default) or `env` | `?format=env` |
| `fields`  | Comma-separated fields to include. `token` is an alias for `access_token` | `?fields=token,expires_in&format=env` |

### Examples with parameters

```bash
# Get only the access token in .env format
curl -v 'http://localhost:8091/api/gmail/token?fields=token&format=env' > token.env

# Add extra scopes
curl -v 'http://localhost:8091/api/google/token?scopes=https://www.googleapis.com/auth/drive.metadata.readonly' > token.json
```

## API reference

### `GET /api/{service}/token`

Starts the OAuth2 flow. Returns response headers with `visit-auth-URL` and keeps the connection open. When the user authorizes in the browser, the token is delivered as the response body.

**Response headers:**
- `visit-auth-URL` — the URL to open in your browser
- `notes` — instructions
- `X-Session-Id` — the session identifier

**Query parameters:** `scopes`, `format`, `fields` (see above)

### `GET /auth/{hash}`

Internal redirect endpoint. Maps the masked URL to the real provider authorization URL.

### `GET /api/{provider}/callback`

Internal callback endpoint. Receives the OAuth2 redirect from the provider and signals the waiting curl connection.

## Architecture

```
┌─────────┐   curl -v /api/google/token    ┌──────────────┐
│ Terminal │ ──────────────────────────────▶│              │
│  (curl)  │                                │   Go Server  │
│          │◀──── header: visit-auth-URL ───│   :8091      │
│          │                                │              │
│  Browser │  open /auth/{hash}             │              │
│  ────────│───────────────────────────────▶│              │
│          │◀── 302 redirect to provider ───│              │
│          │                                │              │
│  Provider│  callback /api/google/callback │              │
│  (Google)│───────────────────────────────▶│              │
│          │                                │              │
│ Terminal │◀────── access token ───────────│              │
└─────────┘                                └──────────────┘
```

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `GOOGLE_CLIENT_ID` | For Google services | Google OAuth2 client ID |
| `GOOGLE_CLIENT_SECRET` | For Google services | Google OAuth2 client secret |
| `GOOGLE_REDIRECT_URI` | No | Default: `http://localhost:8091/api/google/callback` |
| `GOOGLE_SCOPES` | No | Default: `openid email profile` |
| `MICROSOFT_CLIENT_ID` | For Microsoft | Microsoft OAuth2 client ID |
| `MICROSOFT_CLIENT_SECRET` | For Microsoft | Microsoft OAuth2 client secret |
| `MICROSOFT_REDIRECT_URI` | No | Default: `http://localhost:8091/api/microsoft/callback` |
| `MICROSOFT_SCOPES` | No | Default: `openid email profile offline_access` |

## Development

```bash
go build -o playground . && ./playground
```

The binary is gitignored. No external dependencies — only the Go standard library.

## License

MIT
