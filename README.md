# Glosos Pion WebRTC Client

A standalone, headless WebRTC client written in Go using [Pion WebRTC](https://github.com/pion/webrtc) (`v4`). It connects to the **Glosos Signaling Server**, establishes peer-to-peer WebRTC connections with other peers (such as the Glosos Web client), and transmits bidirectional text data over a WebRTC `RTCDataChannel`.

## Features

- 🔌 **Headless & Fast**: Runs directly in the terminal with an interactive REPL interface.
- 🔑 **Email / Username Authentication**: Authenticates with `/api/auth/login` using standard username/email + password.
- 🔄 **Silent Token Refresh**: Automatically inspects the JWT expiration claim (`exp`) and silently fetches a new access token via `/api/auth/refresh` 60 seconds before expiration to keep the session alive indefinitely.
- 📡 **Socket.IO v4 Signaling**: Built-in Engine.IO v4 / Socket.IO v4 WebSocket transport for seamless communication with the Glosos backend.
- 🌐 **WebRTC Data Channel (`chat`)**: Transmits text messages and commands (like `/clear`) directly peer-to-peer.
- 🔒 **STUN & Ephemeral TURN Relay Support**: Uses Google STUN servers with optional automatic fetch of ephemeral TURN credentials from `/api/turn/credentials`.
- ⚡ **Auto-Accept Mode**: Automatically accepts incoming connection requests from other devices or browser tabs logged into the same account.

---

## Installation & Build

Ensure you have **Go 1.22+** installed:

```bash
# Build the binary
go build -o glosos-client .
```

---

## Quick Start

### 1. Run the client

```bash
./glosos-client -server http://localhost:5001 -u your_username -p your_password
```

Or pass via environment variables or interactive prompts:

```bash
export GLOSOS_SERVER=http://localhost:5001
export GLOSOS_LOGIN=user@example.com
export GLOSOS_PASSWORD=secretpassword

./glosos-client
```

### 2. Connect with another peer (e.g. Web Client)

1. Open the **Glosos Web Client** (`glosos-signaling`) in your browser and log in with the same account.
2. In the web client, you will see your Pion client listed under **Active Users & Devices** (`pion-cli`).
3. Click **Connect** on the Web Client, or run `/call <socketId>` in the Pion CLI terminal.
4. Once connected, type any text in the terminal to send messages over the WebRTC Data Channel to the web browser in real-time!

---

## CLI Options

| Flag | Shorthand | Environment Variable | Default | Description |
|------|-----------|----------------------|---------|-------------|
| `-server` | | `GLOSOS_SERVER` | `http://localhost:5001` | URL of the Glosos signaling and backend API |
| `-login` | `-u` | `GLOSOS_LOGIN` | | User login (username or email) |
| `-password` | `-p` | `GLOSOS_PASSWORD` | | Account password (prompted if omitted) |
| `-auto-accept` | | | `true` | Automatically accept incoming WebRTC calls |
| `-turn` | | | `false` | Enable TURN relay fallback using ephemeral credentials |
| `-call` | | | `""` | Socket ID of target peer to call immediately on launch |
| `-client-type` | | | `pion-cli` | Client type identifier sent in signaling handshake |
| `-device-model`| | | `Go/Pion Terminal` | Device model description |

---

## Interactive Terminal Commands

While the client is running, you can use the following interactive commands:

- `/users` — List active devices and socket IDs connected under your user account.
- `/call <socketId>` — Initiate a WebRTC connection to a specific peer.
- `/hangup` — Close the active WebRTC connection.
- `/clear` — Send a `/clear` reset command to the remote peer.
- `/help` — Display available commands.
- `/quit` or `/exit` — Gracefully disconnect and exit.
- `<any text>` — Sends the text across the active WebRTC Data Channel.

---

## Architecture Overview

```
glosos-client-pion/
├── main.go                       # CLI entry point, flag parsing & interactive REPL loop
├── internal/
│   ├── auth/
│   │   ├── client.go             # HTTP Auth client (login, token refresh, TURN credentials)
│   │   ├── token.go              # JWT parser & background silent token refresh manager
│   │   ├── token_test.go         # Unit tests for silent auth & token manager
│   │   └── types.go              # Auth models & request/response payloads
│   ├── signaling/
│   │   ├── client.go             # Socket.IO v4 signaling client (Engine.IO v4 over WebSocket)
│   │   └── types.go              # Signaling event payloads (offer, answer, candidate, online-users)
│   ├── webrtc/
│   │   ├── manager.go            # Pion WebRTC PeerConnection manager (SDP exchange, trickle ICE)
│   │   └── datachannel.go        # DataChannel setup, message formatting & transmission
│   └── config/
│       └── config.go             # CLI & runtime configuration
└── README.md
```
