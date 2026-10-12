package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Basistiy/glosos-client-pion/internal/audio"
	"github.com/Basistiy/glosos-client-pion/internal/auth"
	"github.com/Basistiy/glosos-client-pion/internal/config"
	"github.com/Basistiy/glosos-client-pion/internal/display"
	"github.com/Basistiy/glosos-client-pion/internal/esp32"
	"github.com/Basistiy/glosos-client-pion/internal/signaling"
	"github.com/Basistiy/glosos-client-pion/internal/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
	"golang.org/x/term"
)

func main() {
	cfg, err := config.ParseFlags()
	if err != nil {
		log.Fatalf("[Main] Configuration error: %v", err)
	}

	display.SetEnabled(cfg.EnableDisplay)
	if cfg.SendChatBin != "" {
		display.SetSendChatBin(cfg.SendChatBin)
	}

	fmt.Println("======================================================")
	fmt.Println("  Glosos Pion WebRTC Headless Client")
	fmt.Printf("  Signaling Server : %s\n", cfg.ServerURL)
	fmt.Printf("  User Login       : %s\n", cfg.Login)
	fmt.Printf("  Voice Audio      : %v (Full-Duplex Opus 48kHz)\n", cfg.Voice)
	if cfg.Voice && cfg.PTT {
		fmt.Printf("  Push-To-Talk     : Enabled (Rotary Wheel GPIO 5 '%s')\n", cfg.PTTButton)
	}
	if cfg.AudioTestTone {
		fmt.Printf("  Audio Input Mode : Synthetic 440Hz Test Tone\n")
	}
	fmt.Printf("  TURN Relay       : %v\n", cfg.UseTurn)
	fmt.Printf("  Device Display   : %v (e-Paper / LCD)\n", cfg.EnableDisplay)
	if cfg.EnableESP32 {
		fmt.Printf("  ESP32 Hardware   : Enabled (Buttons & CDC Link)\n")
	}
	fmt.Printf("  Auto-Accept Calls: %v\n", cfg.AutoAccept)
	fmt.Printf("  Auto-Connect     : %v\n", cfg.AutoConnect || cfg.CallTarget != "" || cfg.AutoConnectTarget != "")
	if cfg.AutoConnectTarget != "" {
		fmt.Printf("  Target Keyword   : %s\n", cfg.AutoConnectTarget)
	}
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
		audioPipeline   *audio.Pipeline
		esp32Client     *esp32.Client
		onlineUsersMu   sync.RWMutex
		onlineUsers     []signaling.OnlineUser

		autoConnectMu      sync.Mutex
		autoConnectEnabled = cfg.AutoConnect || cfg.CallTarget != "" || cfg.AutoConnectTarget != ""
		autoConnectTimer   *time.Timer
	)

	// 4. Initialize Full-Duplex Audio Pipeline
	if cfg.Voice {
		pipeline, err := audio.NewPipeline(audio.Config{
			Enabled:          true,
			InputDeviceName:  cfg.InputDevice,
			OutputDeviceName: cfg.OutputDevice,
			UseTestTone:      cfg.AudioTestTone,
		})
		if err != nil {
			log.Printf("[Audio] Warning: Failed to initialize audio pipeline: %v. Continuing in text-only mode.", err)
		} else {
			audioPipeline = pipeline
			if err := audioPipeline.Start(); err != nil {
				log.Printf("[Audio] Warning: Failed to start audio devices: %v", err)
			} else {
				log.Println("[Audio] Full-duplex voice pipeline started successfully.")
			}
			defer audioPipeline.Close()

			if cfg.PTT {
				audioPipeline.SetMuted(true)
				log.Printf("[Audio] Push-To-Talk (PTT) active. Microphone muted. Press Rotary Wheel Center click (GPIO 5) to record.")
			}
		}
	}

	// 4.1. Initialize ESP32 Hardware Buttons & CDC Serial Link
	if cfg.EnableESP32 {
		esp32Client = esp32.NewClient(esp32.Config{
			PortName: cfg.ESP32Port,
		})

		// Hook direct display sender to ESP32 CDC Serial
		display.SetDirectSender(func(role display.MsgRole, text string) error {
			if esp32Client == nil || !esp32Client.IsConnected() {
				return errors.New("esp32 serial disconnected")
			}
			cmd := "/agent " + text
			if role == display.RoleUser {
				cmd = "/user " + text
			}
			return esp32Client.SendCommand(cmd)
		})

		// Register button events from ESP32 hardware
		esp32Client.OnButton(func(evt esp32.ButtonEvent) {
			isPTTBtn := evt.ID == cfg.PTTButton ||
				(cfg.PTTButton == "wheel_click" && (evt.ID == "wheel_click" || evt.ID == "wheel" || evt.ID == "center"))

			if isPTTBtn && cfg.PTT {
				if evt.State == "press" {
					if audioPipeline != nil {
						audioPipeline.SetMuted(false)
						audioPipeline.PlayPTTStartSound()
						fmt.Printf("\n\033[1;32m🎙️ [PTT Recording]\033[0m Microphone active (Rotary Wheel held)...\n> ")
					}
				} else if evt.State == "release" {
					if audioPipeline != nil {
						audioPipeline.SetMuted(true)
						fmt.Printf("\n\033[1;33m🔇 [PTT Muted]\033[0m Recording stopped (held for %d ms).\n> ", evt.DurationMs)
					}
				}
			} else {
				switch evt.ID {
				case "boot":
					if evt.State == "press" {
						fmt.Printf("\n[Button] 🔘 BOOT Button (GPIO 0) pressed\n> ")
					}
				case "wheel_up":
					if evt.State == "press" {
						fmt.Printf("\n[Button] ⬆️ Rotary Wheel Up (GPIO 4)\n> ")
					}
				case "wheel_down":
					if evt.State == "press" {
						fmt.Printf("\n[Button] ⬇️ Rotary Wheel Down (GPIO 6)\n> ")
					}
				default:
					if evt.State == "press" {
						fmt.Printf("\n[Button] Button %q pressed\n> ", evt.ID)
					}
				}
			}
		})

		// On disconnect, ensure microphone is safely muted if in PTT mode
		esp32Client.OnDisconnect(func() {
			if cfg.PTT && audioPipeline != nil {
				audioPipeline.SetMuted(true)
			}
		})

		esp32Client.Start()
		defer esp32Client.Close()
	}

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
				strings.Contains(strings.ToLower(u.Username), lowerInput) ||
				strings.HasPrefix(strings.ToLower(u.SocketID), lowerInput) {
				return &u
			}
		}

		return nil
	}

	// Helper to trigger automatic connection / reconnection
	triggerAutoConnect := func() {
		autoConnectMu.Lock()
		defer autoConnectMu.Unlock()

		if !autoConnectEnabled {
			return
		}

		if webrtcMgr != nil && webrtcMgr.IsActive() {
			return
		}

		if autoConnectTimer != nil {
			autoConnectTimer.Stop()
		}

		// 500ms debounce to allow online-users / sockets to stabilize
		autoConnectTimer = time.AfterFunc(500*time.Millisecond, func() {
			if webrtcMgr == nil || webrtcMgr.IsActive() {
				return
			}

			var candidate *signaling.OnlineUser
			if cfg.AutoConnectTarget != "" {
				candidate = resolveTargetPeer(cfg.AutoConnectTarget)
			} else if cfg.CallTarget != "" {
				candidate = resolveTargetPeer(cfg.CallTarget)
			} else {
				// Pick first online peer
				onlineUsersMu.RLock()
				myID := ""
				if signalingClient != nil {
					myID = signalingClient.SocketID()
				}
				for _, u := range onlineUsers {
					if u.SocketID != myID {
						uCopy := u
						candidate = &uCopy
						break
					}
				}
				onlineUsersMu.RUnlock()
			}

			if candidate != nil && signalingClient != nil {
				targetID := candidate.SocketID
				targetName := candidate.Username
				if candidate.DeviceModel != "" {
					targetName += " (" + candidate.DeviceModel + ")"
				} else if candidate.ClientType != "" {
					targetName += " (" + candidate.ClientType + ")"
				}

				log.Printf("[Auto-Connect] Automatically connecting to %s [Socket: %s] ...", targetName, targetID)
				offer, err := webrtcMgr.CreateOfferForCall(targetID, targetName)
				if err != nil {
					log.Printf("[Auto-Connect] Error creating offer: %v", err)
					return
				}
				if err := signalingClient.CallUser(targetID, offer, cfg.UseTurn); err != nil {
					log.Printf("[Auto-Connect] Error sending call: %v", err)
				}
			}
		})
	}

	// 5. Setup WebRTC Manager Callbacks
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
				if audioPipeline != nil {
					audioPipeline.PlayConnectSound()
				}
			} else if state == pionwebrtc.PeerConnectionStateFailed || state == pionwebrtc.PeerConnectionStateClosed || state == pionwebrtc.PeerConnectionStateDisconnected {
				if audioPipeline != nil {
					audioPipeline.PlayDisconnectSound()
				}
				triggerAutoConnect()
			}
		},
		OnDataChannelOpen: func() {
			fmt.Printf("\n\033[1;32m[DataChannel Open]\033[0m Type any message and press Enter to send over WebRTC.\n> ")
		},
		OnDataChannelClose: func() {
			fmt.Printf("\n\033[1;31m[DataChannel Closed]\033[0m WebRTC session ended.\n> ")
			triggerAutoConnect()
		},
		OnDataChannelMsg: func(sender, text string, raw []byte) {
			webrtc.DefaultConsoleMessageFormatter(sender, text, raw)

			if cfg.EnableDisplay {
				isAgent := strings.Contains(strings.ToLower(sender), "agent") ||
					strings.Contains(strings.ToLower(sender), "assistant")
				isUser := strings.HasPrefix(strings.ToLower(sender), "me") ||
					strings.Contains(strings.ToLower(sender), "transcription")

				msgText := text
				if len(raw) > 0 {
					var parsed struct {
						Type    string `json:"type"`
						Role    string `json:"role"`
						Text    string `json:"text"`
						Content string `json:"content"`
						Message string `json:"message"`
					}
					if err := json.Unmarshal(raw, &parsed); err == nil {
						if parsed.Type == "agent" || parsed.Type == "agent_response" || parsed.Role == "agent" || parsed.Role == "assistant" {
							isAgent = true
							isUser = false
						} else if parsed.Type == "transcription" || parsed.Type == "user" || parsed.Role == "user" {
							isUser = true
							isAgent = false
						}
						c := parsed.Text
						if c == "" {
							c = parsed.Content
						}
						if c == "" {
							c = parsed.Message
						}
						if c != "" {
							msgText = c
						}
					}
				}

				if msgText != "" {
					if isAgent {
						display.ShowAgentMessage(msgText)
					} else if isUser {
						display.ShowUserMessage(msgText)
					}
				}
			}
		},
		OnRemoteAudioFrame: func(opusData []byte) {
			if audioPipeline != nil {
				audioPipeline.PushRemoteOpusFrame(opusData)
			}
		},
		OnAudioTrackOpen: func() {
			fmt.Printf("\n\033[1;32m[Audio Track Open]\033[0m Full-duplex voice stream active with remote peer.\n> ")
		},
		OnAudioTrackClose: func() {
			fmt.Printf("\n\033[1;33m[Audio Track Closed]\033[0m Remote audio track closed.\n> ")
		},
	}

	webrtcMgr = webrtc.NewManager(webrtcCallbacks, turnCreds, cfg.UseTurn, cfg.Voice)
	defer webrtcMgr.Close()

	// Link Audio Pipeline outbound frames to WebRTC local audio track
	if audioPipeline != nil {
		audioPipeline.SetLocalOpusHandler(func(opusFrame []byte) {
			if webrtcMgr != nil && webrtcMgr.IsActive() {
				_ = webrtcMgr.SendAudioSample(opusFrame, time.Duration(audio.FrameDurationMs)*time.Millisecond)
			}
		})
	}

	// 6. Setup Signaling Handlers
	signalingHandlers := signaling.Handlers{
		OnConnected: func(socketID string) {
			log.Printf("[Signaling] Ready to receive and initiate calls.")
			if audioPipeline != nil {
				audioPipeline.PlayServerReadySound()
			}
			triggerAutoConnect()
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

			// Only print online users in console when not in an active WebRTC call to avoid cluttering chat
			if !webrtcMgr.IsActive() {
				if len(others) > 0 {
					log.Printf("[Signaling] %d other peer(s) online (type /call <number> to connect):\n  %s",
						len(others), strings.Join(others, "\n  "))
				} else {
					log.Printf("[Signaling] No other devices online.")
				}
			}

			// Trigger auto-connection if enabled
			triggerAutoConnect()
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
			triggerAutoConnect()
		},
		OnCallFailed: func(payload signaling.CallFailedPayload) {
			log.Printf("[Signaling] Call failed to socket %s: %s", payload.TargetSocketID, payload.Reason)
			webrtcMgr.Close()
			triggerAutoConnect()
		},
	}

	// 7. Connect Signaling Client
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

	// 8. Handle SIGINT / SIGTERM for Graceful Exit
	cleanupOnce := sync.Once{}
	cleanup := func() {
		cleanupOnce.Do(func() {
			fmt.Println("\n[Main] Exiting Glosos client...")
			if esp32Client != nil {
				esp32Client.Close()
			}
			if webrtcMgr != nil && webrtcMgr.TargetSocketID() != "" && signalingClient != nil {
				_ = signalingClient.HangUp(webrtcMgr.TargetSocketID())
			}
			display.Close()
			if audioPipeline != nil {
				audioPipeline.Close()
			}
			if webrtcMgr != nil {
				webrtcMgr.Close()
			}
			if signalingClient != nil {
				signalingClient.Close()
			}
			if tokenMgr != nil {
				tokenMgr.Stop()
			}
		})
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		cleanup()
		os.Exit(0)
	}()

	// 9. Headless / Daemon Mode or Interactive CLI REPL Loop
	if cfg.Daemon || !term.IsTerminal(int(os.Stdin.Fd())) {
		log.Println("[Main] Running in background / daemon mode. Waiting for termination signal...")
		select {}
	}

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

			case "autoconnect":
				if len(parts) > 1 {
					arg := strings.ToLower(parts[1])
					autoConnectMu.Lock()
					if arg == "on" || arg == "true" || arg == "1" {
						autoConnectEnabled = true
						fmt.Println("[Auto-Connect] Enabled.")
					} else if arg == "off" || arg == "false" || arg == "0" {
						autoConnectEnabled = false
						fmt.Println("[Auto-Connect] Disabled.")
					} else {
						cfg.AutoConnectTarget = parts[1]
						autoConnectEnabled = true
						fmt.Printf("[Auto-Connect] Enabled with target '%s'.\n", parts[1])
					}
					autoConnectMu.Unlock()
					triggerAutoConnect()
				} else {
					autoConnectMu.Lock()
					status := "disabled"
					if autoConnectEnabled {
						status = "enabled"
						if cfg.AutoConnectTarget != "" {
							status += fmt.Sprintf(" (target: %s)", cfg.AutoConnectTarget)
						}
					}
					autoConnectMu.Unlock()
					fmt.Printf("[Auto-Connect] Current status: %s\n", status)
				}

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

			case "mute":
				if audioPipeline != nil {
					audioPipeline.SetMuted(true)
					fmt.Println("[Audio] \033[1;31m[MUTED]\033[0m Microphone audio transmission paused.")
				} else {
					fmt.Println("[Audio] Voice is disabled.")
				}

			case "unmute":
				if audioPipeline != nil {
					audioPipeline.SetMuted(false)
					fmt.Println("[Audio] \033[1;32m[UNMUTED]\033[0m Microphone audio transmission active.")
				} else {
					fmt.Println("[Audio] Voice is disabled.")
				}

			case "deafen":
				if audioPipeline != nil {
					audioPipeline.SetDeafened(true)
					fmt.Println("[Audio] \033[1;31m[DEAFENED]\033[0m Speaker audio output muted.")
				} else {
					fmt.Println("[Audio] Voice is disabled.")
				}

			case "undeafen":
				if audioPipeline != nil {
					audioPipeline.SetDeafened(false)
					fmt.Println("[Audio] \033[1;32m[UNDEAFENED]\033[0m Speaker audio output restored.")
				} else {
					fmt.Println("[Audio] Voice is disabled.")
				}

			case "audio":
				if audioPipeline != nil {
					fmt.Println("\n--- Audio Pipeline Status ---")
					micStatus := "\033[1;32mActive (Streaming)\033[0m"
					if audioPipeline.IsMuted() {
						if cfg.PTT {
							micStatus = "\033[1;33mPTT Idle (Muted - hold Rotary Center click to record)\033[0m"
						} else {
							micStatus = "\033[1;31mMuted\033[0m"
						}
					}
					spkStatus := "\033[1;32mActive (Playing)\033[0m"
					if audioPipeline.IsDeafened() {
						spkStatus = "\033[1;31mDeafened\033[0m"
					}
					fmt.Printf("  Microphone  : %s\n", micStatus)
					fmt.Printf("  Speaker     : %s\n", spkStatus)
					fmt.Printf("  Push-To-Talk: %v\n", cfg.PTT)
					if cfg.PTT {
						fmt.Printf("  PTT Button  : %s (GPIO 5)\n", cfg.PTTButton)
					}
					if esp32Client != nil {
						espStatus := "\033[1;31mDisconnected\033[0m"
						if esp32Client.IsConnected() {
							espStatus = fmt.Sprintf("\033[1;32mConnected\033[0m (%s)", esp32Client.PortName())
						}
						fmt.Printf("  ESP32 Link  : %s\n", espStatus)
					}
					fmt.Printf("  Codec       : Opus 48,000 Hz, Mono, 20ms frames\n")
					if cfg.AudioTestTone {
						fmt.Printf("  Mode        : Synthetic 440Hz Test Tone\n")
					}
					fmt.Println("-----------------------------")
				} else {
					fmt.Println("[Audio] Voice is disabled. Launch with default or -voice=true to enable.")
				}

			case "devices":
				if audioPipeline != nil {
					devs, err := audioPipeline.ListDevices()
					if err != nil {
						fmt.Printf("[Audio] Failed to list devices: %v\n", err)
					} else {
						fmt.Println("\n--- Audio Devices ---")
						for _, d := range devs {
							def := ""
							if d.IsDefault {
								def = " \033[1;32m(Default)\033[0m"
							}
							fmt.Printf("  [%s] %s%s\n", strings.ToUpper(d.Type), d.Name, def)
						}
						fmt.Println("---------------------")
					}
				} else {
					fmt.Println("[Audio] Audio pipeline is not initialized.")
				}

			case "hangup":
				autoConnectMu.Lock()
				autoConnectEnabled = false
				autoConnectMu.Unlock()

				targetID := webrtcMgr.TargetSocketID()
				if targetID != "" {
					_ = signalingClient.HangUp(targetID)
					log.Printf("[Call] Hung up call to socket %s (Auto-connect paused).", targetID)
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

			case "chime", "sound":
				if audioPipeline != nil {
					audioPipeline.PlayConnectSound()
					fmt.Println("[Audio] Played connection chime.")
				} else {
					fmt.Println("[Audio] Voice is disabled.")
				}

			case "display":
				testMsg := "Display test from Glosos!"
				isUser := false
				if len(parts) > 1 {
					if strings.ToLower(parts[1]) == "user" {
						isUser = true
						if len(parts) > 2 {
							testMsg = strings.Join(parts[2:], " ")
						}
					} else if strings.ToLower(parts[1]) == "agent" {
						if len(parts) > 2 {
							testMsg = strings.Join(parts[2:], " ")
						}
					} else {
						testMsg = strings.Join(parts[1:], " ")
					}
				}
				if isUser {
					display.ShowUserMessage(testMsg)
					fmt.Printf("[Display] Triggered user bubble with text: %q\n", testMsg)
				} else {
					display.ShowAgentResponse(testMsg)
					fmt.Printf("[Display] Triggered agent bubble with text: %q\n", testMsg)
				}

			case "quit", "exit":
				cleanup()
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
					if cfg.EnableDisplay {
						display.ShowUserMessage(rawLine)
					}
				}
			} else {
				fmt.Println("\033[1;33m[Warning]\033[0m DataChannel is not open. Connect to a peer first with /call <number|socketId> or click Connect in the Web client.")
			}
		}

		fmt.Print("> ")
	}

	cleanup()
}

func printHelp() {
	fmt.Println("\nAvailable CLI Commands:")
	fmt.Println("  /users              - List active online devices/peers with numbers [1], [2]...")
	fmt.Println("  /call <1|2|socket>  - Initiate WebRTC connection (e.g. /call 1)")
	fmt.Println("  /autoconnect [on|off] - Toggle or configure automatic connection")
	fmt.Println("  /mute / /unmute     - Mute or unmute microphone (or toggle PTT override)")
	fmt.Println("  /deafen / /undeafen - Mute or unmute speaker audio playback")
	fmt.Println("  /audio              - Show active voice/audio and PTT hardware status")
	fmt.Println("  /sound              - Play test connection chime")
	fmt.Println("  /display [user|agent] [text] - Test printing a user or agent bubble on device display")
	fmt.Println("  /devices            - List available audio input/output devices")
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
