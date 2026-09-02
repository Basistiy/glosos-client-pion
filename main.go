package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/Basistiy/glosos-client-pion/internal/auth"
	"github.com/Basistiy/glosos-client-pion/internal/config"
	"github.com/Basistiy/glosos-client-pion/internal/signaling"
	"github.com/Basistiy/glosos-client-pion/internal/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
)

func main() {
	cfg, err := config.ParseFlags()
	if err != nil {
		log.Fatalf("[Main] Configuration error: %v", err)
	}

	fmt.Println("======================================================")
	fmt.Println("  Glosos Pion WebRTC Headless Client")
	fmt.Printf("  Signaling Server : %s\n", cfg.ServerURL)
	fmt.Printf("  User Login       : %s\n", cfg.Login)
	fmt.Printf("  TURN Relay       : %v\n", cfg.UseTurn)
	fmt.Printf("  Auto-Accept Calls: %v\n", cfg.AutoAccept)
	fmt.Println("======================================================")

	// 1. Authenticate with Glosos Backend
	authClient := auth.NewClient(0)
	log.Printf("[Auth] Logging in as '%s' ...", cfg.Login)
	loginResp, err := authClient.Login(cfg.ServerURL, cfg.Login, cfg.Password)
	if err != nil {
		log.Fatalf("[Auth] Login failed: %v", err)
	}

	log.Printf("[Auth] Successfully logged in as \033[1;32m%s\033[0m (User ID: %d, Email: %s)",
		loginResp.User.Username, loginResp.User.ID, loginResp.User.Email)

	// 2. Initialize Silent Token Manager
	tokenMgr := auth.NewTokenManager(
		authClient,
		cfg.ServerURL,
		loginResp.Token,
		loginResp.RefreshToken,
		loginResp.User,
	)
	tokenMgr.Start()
	defer tokenMgr.Stop()

	// 3. Optional: Fetch Ephemeral TURN Credentials
	var turnCreds *auth.TurnCredentialsResponse
	if cfg.UseTurn {
		log.Printf("[Auth] Fetching ephemeral TURN credentials...")
		turnCreds, err = authClient.GetTurnCredentials(cfg.ServerURL, tokenMgr.GetToken())
		if err != nil {
			log.Printf("[Auth] Warning: Failed to fetch TURN credentials (%v). Continuing with STUN.", err)
		} else {
			log.Printf("[Auth] Ephemeral TURN credentials received (URL: %s, User: %s)", turnCreds.URL, turnCreds.Username)
		}
	}

	var (
		signalingClient *signaling.Client
		webrtcMgr       *webrtc.Manager
		onlineUsersMu   sync.RWMutex
		onlineUsers     []signaling.OnlineUser
	)

	// Helper to resolve peer target from input (number index, device keyword, or socketId)
	resolveTargetPeer := func(input string) *signaling.OnlineUser {
		onlineUsersMu.RLock()
		defer onlineUsersMu.RUnlock()

		myID := ""
		if signalingClient != nil {
			myID = signalingClient.SocketID()
		}

		// Filter other peers
		var others []signaling.OnlineUser
		for _, u := range onlineUsers {
			if u.SocketID != myID {
				others = append(others, u)
			}
		}

		if len(others) == 0 {
			return nil
		}

		// 1. Check if number index (1-based)
		if idx, err := strconv.Atoi(input); err == nil {
			if idx >= 1 && idx <= len(others) {
				return &others[idx-1]
			}
		}

		// 2. Exact match on socket ID
		for _, u := range others {
			if u.SocketID == input {
				return &u
			}
		}

		// 3. Case-insensitive substring match on clientType, deviceModel, or socket prefix
		lowerInput := strings.ToLower(input)
		for _, u := range others {
			if strings.Contains(strings.ToLower(u.DeviceModel), lowerInput) ||
				strings.Contains(strings.ToLower(u.ClientType), lowerInput) ||
				strings.HasPrefix(strings.ToLower(u.SocketID), lowerInput) {
				return &u
			}
		}

		return nil
	}

	// 4. Setup WebRTC Manager Callbacks
	webrtcCallbacks := webrtc.Callbacks{
		OnLocalICECandidate: func(targetSocketID string, candidate webrtc.ICECandidateJSON) {
			if signalingClient != nil {
				if err := signalingClient.SendIceCandidate(targetSocketID, candidate); err != nil {
					log.Printf("[Signaling] Failed to send ICE candidate: %v", err)
				}
			}
		},
		OnConnectionState: func(state pionwebrtc.PeerConnectionState) {
			if state == pionwebrtc.PeerConnectionStateConnected {
				fmt.Printf("\n\033[1;32m[WebRTC Connected]\033[0m P2P Connection established with peer!\n> ")
			}
		},
		OnDataChannelOpen: func() {
			fmt.Printf("\n\033[1;32m[DataChannel Open]\033[0m Type any message and press Enter to send over WebRTC.\n> ")
		},
		OnDataChannelClose: func() {
			fmt.Printf("\n\033[1;31m[DataChannel Closed]\033[0m WebRTC session ended.\n> ")
		},
		OnDataChannelMsg: webrtc.DefaultConsoleMessageFormatter,
	}

	webrtcMgr = webrtc.NewManager(webrtcCallbacks, turnCreds, cfg.UseTurn)
	defer webrtcMgr.Close()

	// 5. Setup Signaling Handlers
	signalingHandlers := signaling.Handlers{
		OnConnected: func(socketID string) {
			log.Printf("[Signaling] Ready to receive and initiate calls.")
			if cfg.CallTarget != "" {
				go func() {
					target := resolveTargetPeer(cfg.CallTarget)
					targetID := cfg.CallTarget
					targetName := "Peer"
					if target != nil {
						targetID = target.SocketID
						targetName = target.Username
						if target.DeviceModel != "" {
							targetName += " (" + target.DeviceModel + ")"
						}
					}

					log.Printf("[Call] Calling target %s (%s) immediately...", targetName, targetID)
					offer, err := webrtcMgr.CreateOfferForCall(targetID, targetName)
					if err != nil {
						log.Printf("[Call] Failed to create offer: %v", err)
						return
					}
					if err := signalingClient.CallUser(targetID, offer, cfg.UseTurn); err != nil {
						log.Printf("[Call] Failed to send call-user offer: %v", err)
					}
				}()
			}
		},
		OnOnlineUsers: func(users []signaling.OnlineUser) {
			onlineUsersMu.Lock()
			onlineUsers = users
			onlineUsersMu.Unlock()

			myID := signalingClient.SocketID()
			var others []string
			idx := 1
			for _, u := range users {
				if u.SocketID != myID {
					desc := fmt.Sprintf("[%d] %s", idx, u.Username)
					if u.ClientType != "" || u.DeviceModel != "" {
						desc += " ("
						if u.ClientType != "" {
							desc += u.ClientType
						}
						if u.DeviceModel != "" {
							desc += ", " + u.DeviceModel
						}
						desc += ")"
					}
					desc += fmt.Sprintf(" [Socket: %s]", u.SocketID)
					others = append(others, desc)
					idx++
				}
			}

			if len(others) > 0 {
				log.Printf("[Signaling] %d other peer(s) online (type /call <number> to connect):\n  %s",
					len(others), strings.Join(others, "\n  "))
			} else {
				log.Printf("[Signaling] No other devices online. Log into the Web client to see it here.")
			}
		},
		OnIncomingCall: func(payload signaling.IncomingCallPayload) {
			log.Printf("[Signaling] Incoming call from \033[1;36m%s\033[0m (Socket ID: %s, TURN: %v)",
				payload.CallerUsername, payload.CallerSocketID, payload.UseTurn)

			if cfg.AutoAccept {
				log.Printf("[Call] Auto-accepting call from %s ...", payload.CallerUsername)
				answer, err := webrtcMgr.AcceptIncomingCall(payload.CallerSocketID, payload.CallerUsername, payload.Offer)
				if err != nil {
					log.Printf("[Call] Failed to accept incoming call: %v", err)
					return
				}
				if err := signalingClient.MakeAnswer(payload.CallerSocketID, answer); err != nil {
					log.Printf("[Call] Failed to send answer: %v", err)
				}
			} else {
				fmt.Printf("\n[Incoming Call] From %s (%s). Type '/call %s' to answer.\n> ",
					payload.CallerUsername, payload.CallerSocketID, payload.CallerSocketID)
			}
		},
		OnCallAccepted: func(payload signaling.CallAcceptedPayload) {
			log.Printf("[Signaling] Call accepted by peer \033[1;36m%s\033[0m (Socket ID: %s)",
				payload.AnswererUsername, payload.AnswererSocketID)
			if err := webrtcMgr.HandleCallAccepted(payload.AnswererSocketID, payload.Answer); err != nil {
				log.Printf("[Call] Error setting remote description from answer: %v", err)
			}
		},
		OnIceCandidate: func(payload signaling.IceCandidatePayload) {
			if err := webrtcMgr.AddRemoteICECandidate(payload.Candidate); err != nil {
				log.Printf("[WebRTC] Error adding remote ICE candidate: %v", err)
			}
		},
		OnHangUp: func(payload signaling.HangUpPayload) {
			log.Printf("[Signaling] Peer hung up the connection.")
			webrtcMgr.Close()
		},
		OnCallFailed: func(payload signaling.CallFailedPayload) {
			log.Printf("[Signaling] Call failed to socket %s: %s", payload.TargetSocketID, payload.Reason)
			webrtcMgr.Close()
		},
	}

	// 6. Connect Signaling Client
	signalingClient = signaling.NewClient(
		cfg.ServerURL,
		tokenMgr.GetToken(),
		cfg.ClientType,
		cfg.DeviceModel,
		signalingHandlers,
	)

	// Keep signaling token updated on silent refresh
	tokenMgr.OnTokenRefreshed(func(newToken string) {
		signalingClient.UpdateToken(newToken)
	})

	if err := signalingClient.Connect(); err != nil {
		log.Fatalf("[Signaling] Failed to connect: %v", err)
	}
	defer signalingClient.Close()

	// 7. Handle SIGINT / SIGTERM for Graceful Exit
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[Main] Exiting Glosos client...")
		if webrtcMgr.TargetSocketID() != "" {
			_ = signalingClient.HangUp(webrtcMgr.TargetSocketID())
		}
		webrtcMgr.Close()
		signalingClient.Close()
		tokenMgr.Stop()
		os.Exit(0)
	}()

	// 8. Interactive CLI REPL Loop
	printHelp()
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")

	for scanner.Scan() {
		rawLine := strings.TrimSpace(scanner.Text())
		if rawLine == "" {
			fmt.Print("> ")
			continue
		}

		// Normalize line: strip leading slashes or extra whitespace
		isCommand := strings.HasPrefix(rawLine, "/")
		cleaned := strings.TrimLeft(rawLine, "/")
		parts := strings.Fields(cleaned)
		cmd := ""
		if len(parts) > 0 {
			cmd = strings.ToLower(parts[0])
		}

		if isCommand {
			switch cmd {
			case "help":
				printHelp()

			case "users":
				onlineUsersMu.RLock()
				myID := signalingClient.SocketID()
				fmt.Println("\n--- Online Devices/Peers ---")
				idx := 1
				for _, u := range onlineUsers {
					if u.SocketID == myID {
						fmt.Printf("  * %s (You, Socket: %s)\n", u.Username, u.SocketID)
					} else {
						clientInfo := u.ClientType
						if u.DeviceModel != "" {
							clientInfo += ", " + u.DeviceModel
						}
						fmt.Printf("  [%d] %s [%s] (Socket: %s)\n", idx, u.Username, clientInfo, u.SocketID)
						idx++
					}
				}
				if idx == 1 {
					fmt.Println("  (No other devices online. Log into the Web client to see it here.)")
				} else {
					fmt.Println("\n  Tip: You can call a peer by number, e.g.: /call 1")
				}
				fmt.Println("----------------------------")
				onlineUsersMu.RUnlock()

			case "call":
				if len(parts) < 2 {
					fmt.Println("Usage: /call <number|keyword|socketId> (e.g. /call 1)")
				} else {
					targetArg := parts[1]
					target := resolveTargetPeer(targetArg)

					var targetSocketID, targetName string
					if target != nil {
						targetSocketID = target.SocketID
						targetName = target.Username
						if target.DeviceModel != "" {
							targetName += " (" + target.DeviceModel + ")"
						} else if target.ClientType != "" {
							targetName += " (" + target.ClientType + ")"
						}
					} else {
						targetSocketID = targetArg
						targetName = "Peer-" + targetArg[:min(4, len(targetArg))]
					}

					log.Printf("[Call] Initiating WebRTC call to %s [Socket: %s] ...", targetName, targetSocketID)
					offer, err := webrtcMgr.CreateOfferForCall(targetSocketID, targetName)
					if err != nil {
						log.Printf("[Call] Error creating offer: %v", err)
					} else {
						if err := signalingClient.CallUser(targetSocketID, offer, cfg.UseTurn); err != nil {
							log.Printf("[Call] Error sending call: %v", err)
						} else {
							log.Printf("[Call] Offer sent to %s. Waiting for answer...", targetName)
						}
					}
				}

			case "hangup":
				targetID := webrtcMgr.TargetSocketID()
				if targetID != "" {
					_ = signalingClient.HangUp(targetID)
					log.Printf("[Call] Hung up call to socket %s.", targetID)
				}
				webrtcMgr.Close()

			case "clear":
				if dcm := webrtcMgr.DataChannelManager(); dcm != nil && dcm.IsOpen() {
					if err := dcm.SendText("/clear"); err != nil {
						fmt.Printf("Failed to send /clear: %v\n", err)
					} else {
						fmt.Println("[Sent] /clear command sent to remote peer.")
					}
				} else {
					fmt.Println("DataChannel is not open.")
				}

			case "quit", "exit":
				fmt.Println("Exiting...")
				if targetID := webrtcMgr.TargetSocketID(); targetID != "" {
					_ = signalingClient.HangUp(targetID)
				}
				webrtcMgr.Close()
				signalingClient.Close()
				tokenMgr.Stop()
				return

			default:
				fmt.Printf("Unknown command: /%s. Type /help for available commands.\n", cmd)
			}
		} else {
			// Regular text message
			dcm := webrtcMgr.DataChannelManager()
			if dcm != nil && dcm.IsOpen() {
				if err := dcm.SendText(rawLine); err != nil {
					fmt.Printf("[Error] Failed to send message: %v\n", err)
				} else {
					// Echo message locally
					fmt.Printf("[me] %s\n", rawLine)
				}
			} else {
				fmt.Println("\033[1;33m[Warning]\033[0m DataChannel is not open. Connect to a peer first with /call <number|socketId> or click Connect in the Web client.")
			}
		}

		fmt.Print("> ")
	}
}

func printHelp() {
	fmt.Println("\nAvailable CLI Commands:")
	fmt.Println("  /users              - List active online devices/peers with numbers [1], [2]...")
	fmt.Println("  /call <1|2|socket>  - Initiate WebRTC connection (e.g. /call 1)")
	fmt.Println("  /hangup             - End the active connection")
	fmt.Println("  /clear              - Send /clear reset command")
	fmt.Println("  /help               - Show this help message")
	fmt.Println("  /quit or /exit      - Exit the application")
	fmt.Println("  <any text>          - Transmit text over WebRTC Data Channel")
	fmt.Println()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
