package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// Config holds runtime options for the Pion client.
type Config struct {
	ServerURL         string
	Login             string
	Password          string
	UseTurn           bool
	AutoAccept        bool
	AutoConnect       bool
	AutoConnectTarget string
	CallTarget        string
	ClientType        string
	DeviceModel       string
	Voice             bool
	InputDevice       string
	OutputDevice      string
	AudioTestTone     bool
	Daemon            bool
	EnableDisplay     bool
}

// ParseFlags parses command-line flags and environment variables.
func ParseFlags() (*Config, error) {
	cfg := &Config{}

	flag.StringVar(&cfg.ServerURL, "server", getEnvDefault("GLOSOS_SERVER", "http://localhost:5001"), "Signaling & API server URL")
	flag.StringVar(&cfg.Login, "login", getEnvDefault("GLOSOS_LOGIN", ""), "Login identifier (username or email)")
	flag.StringVar(&cfg.Login, "u", getEnvDefault("GLOSOS_LOGIN", ""), "Login identifier (shorthand)")
	flag.StringVar(&cfg.Password, "password", getEnvDefault("GLOSOS_PASSWORD", ""), "Account password")
	flag.StringVar(&cfg.Password, "p", getEnvDefault("GLOSOS_PASSWORD", ""), "Account password (shorthand)")
	flag.BoolVar(&cfg.UseTurn, "turn", false, "Use TURN relay servers for WebRTC")
	flag.BoolVar(&cfg.AutoAccept, "auto-accept", true, "Automatically accept incoming calls from other peers/tabs")
	flag.BoolVar(&cfg.AutoConnect, "auto-connect", getEnvBoolDefault("GLOSOS_AUTO_CONNECT", false), "Automatically connect to online peer (or target)")
	flag.StringVar(&cfg.AutoConnectTarget, "auto-connect-target", getEnvDefault("GLOSOS_AUTO_CONNECT_TARGET", ""), "Target keyword/identifier for auto-connection (e.g. 'mac-server' or 'web')")
	flag.StringVar(&cfg.AutoConnectTarget, "target", getEnvDefault("GLOSOS_AUTO_CONNECT_TARGET", ""), "Target keyword/identifier for auto-connection (shorthand)")
	flag.StringVar(&cfg.CallTarget, "call", "", "Socket ID or peer index to call immediately upon connection")
	flag.StringVar(&cfg.ClientType, "client-type", "pion-cli", "Client type identifier for signaling")
	flag.StringVar(&cfg.DeviceModel, "device-model", "Go/Pion Terminal", "Device model identifier for signaling")
	flag.BoolVar(&cfg.Daemon, "daemon", getEnvBoolDefault("GLOSOS_DAEMON", false), "Run in daemon/background mode (no interactive prompt)")
	flag.BoolVar(&cfg.Daemon, "headless", getEnvBoolDefault("GLOSOS_DAEMON", false), "Run in headless mode (alias)")
	flag.BoolVar(&cfg.EnableDisplay, "display", getEnvBoolDefault("GLOSOS_DISPLAY", true), "Enable printing agent responses on device display")

	// Audio / Voice Flags
	flag.BoolVar(&cfg.Voice, "voice", getEnvBoolDefault("GLOSOS_VOICE", true), "Enable full-duplex WebRTC voice transfer")
	flag.BoolVar(&cfg.Voice, "audio", getEnvBoolDefault("GLOSOS_VOICE", true), "Enable full-duplex WebRTC voice transfer (alias)")
	flag.StringVar(&cfg.InputDevice, "input-device", getEnvDefault("GLOSOS_INPUT_DEVICE", ""), "Audio capture/microphone device name")
	flag.StringVar(&cfg.OutputDevice, "output-device", getEnvDefault("GLOSOS_OUTPUT_DEVICE", ""), "Audio playback/speaker device name")
	flag.BoolVar(&cfg.AudioTestTone, "audio-test-tone", false, "Generate 440Hz test sine tone instead of microphone")

	flag.Parse()

	// Prompt for login if not supplied
	if strings.TrimSpace(cfg.Login) == "" {
		fmt.Print("Enter login (email or username): ")
		var loginInput string
		_, err := fmt.Scanln(&loginInput)
		if err != nil && err.Error() != "unexpected newline" {
			return nil, fmt.Errorf("failed to read login: %w", err)
		}
		cfg.Login = strings.TrimSpace(loginInput)
	}

	if cfg.Login == "" {
		return nil, fmt.Errorf("login identifier cannot be empty")
	}

	// Prompt for password if not supplied
	if strings.TrimSpace(cfg.Password) == "" {
		fmt.Printf("Enter password for %s: ", cfg.Login)
		bytePassword, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return nil, fmt.Errorf("failed to read password: %w", err)
		}
		cfg.Password = strings.TrimSpace(string(bytePassword))
	}

	if cfg.Password == "" {
		return nil, fmt.Errorf("password cannot be empty")
	}

	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBoolDefault(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "true" || v == "1" || v == "yes"
	}
	return def
}
