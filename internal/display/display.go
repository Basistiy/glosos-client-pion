package display

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MaxCharsPerLine    = 15 // 128px screen / 8px font width with margins (legacy fallback)
	MaxLinesPerPage    = 11 // 128px screen / 10px line height (legacy fallback)
	PageDwellTime      = 4500 * time.Millisecond
	WordPairDwellTime  = 500 * time.Millisecond
	FinalPairDwellTime = 1200 * time.Millisecond
	MaxRunesForBigFont = 8 // Font size 2 is 16px wide: 128 / 16 = 8 characters max width
)

// MsgRole represents the origin of the message bubble.
type MsgRole int

const (
	RoleUser MsgRole = iota
	RoleAgent
)

func (r MsgRole) String() string {
	switch r {
	case RoleUser:
		return "user"
	case RoleAgent:
		return "agent"
	default:
		return "unknown"
	}
}

type displayTask struct {
	role MsgRole
	text string
}

// WordPair represents a single display screen showing 1 or 2 words centered, one under the other.
type WordPair struct {
	Text string
	Size int
}

var (
	displayMu        sync.Mutex
	enabled          = true
	configuredBin    string
	cfgBinMu         sync.Mutex
	sendChatBin      string
	sendChatSearched bool
	cachedLcdBin     string
	lcdBinSearched   bool

	taskChan        = make(chan displayTask, 64)
	workerOnce      sync.Once
	stopChan        = make(chan struct{})
	workerMu        sync.Mutex
	isWorkerRunning bool
	workerDone      sync.WaitGroup
)

// SetEnabled enables or disables printing to the device display.
func SetEnabled(en bool) {
	displayMu.Lock()
	defer displayMu.Unlock()
	enabled = en
}

// IsEnabled returns true if display output is enabled.
func IsEnabled() bool {
	displayMu.Lock()
	defer displayMu.Unlock()
	return enabled
}

// SetSendChatBin overrides the path to the send_chat binary for e-Paper control.
func SetSendChatBin(path string) {
	cfgBinMu.Lock()
	defer cfgBinMu.Unlock()
	configuredBin = strings.TrimSpace(path)
	sendChatSearched = false
}

// findSendChat locates the send_chat binary on OpenStick Linux.
func findSendChat() string {
	cfgBinMu.Lock()
	defer cfgBinMu.Unlock()

	if sendChatSearched {
		return sendChatBin
	}
	sendChatSearched = true

	// 1. Explicitly configured via CLI flag
	if configuredBin != "" {
		if fi, err := os.Stat(configuredBin); err == nil && !fi.IsDir() {
			sendChatBin = configuredBin
			return sendChatBin
		}
		if lp, err := exec.LookPath(configuredBin); err == nil {
			sendChatBin = lp
			return sendChatBin
		}
	}

	// 2. Configured via environment variable
	if envBin := os.Getenv("GLOSOS_SEND_CHAT_BIN"); envBin != "" {
		if fi, err := os.Stat(envBin); err == nil && !fi.IsDir() {
			sendChatBin = envBin
			return sendChatBin
		}
		if lp, err := exec.LookPath(envBin); err == nil {
			sendChatBin = lp
			return sendChatBin
		}
	}

	// 3. Standard OpenStick Linux locations
	candidates := []string{
		"/usr/local/bin/send_chat",
		"/usr/bin/send_chat",
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			sendChatBin = c
			return sendChatBin
		}
	}

	// 4. In system PATH
	if lp, err := exec.LookPath("send_chat"); err == nil {
		sendChatBin = lp
		return sendChatBin
	}

	return ""
}

// findLcdCtl locates the legacy lcd_ctl binary on the system.
func findLcdCtl() string {
	if lcdBinSearched {
		return cachedLcdBin
	}
	lcdBinSearched = true

	candidates := []string{
		"/usr/local/bin/lcd_ctl",
		"/usr/bin/lcd_ctl",
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			cachedLcdBin = c
			return cachedLcdBin
		}
	}

	if lp, err := exec.LookPath("lcd_ctl"); err == nil {
		cachedLcdBin = lp
		return cachedLcdBin
	}

	return ""
}

// startWorker initializes the background FIFO display dispatcher once.
func startWorker() {
	workerOnce.Do(func() {
		workerMu.Lock()
		isWorkerRunning = true
		workerMu.Unlock()
		workerDone.Add(1)
		go runDisplayWorker()
	})
}

// enqueueTask dispatches a task to the background worker channel without blocking.
func enqueueTask(task displayTask) {
	if !IsEnabled() {
		return
	}
	startWorker()

	workerMu.Lock()
	running := isWorkerRunning
	workerMu.Unlock()
	if !running {
		return
	}

	select {
	case taskChan <- task:
	default:
		// Channel buffer is full: drop the oldest task to prioritize newer conversation
		select {
		case <-taskChan:
		default:
		}
		taskChan <- task
	}
}

// ShowUserMessage formats and asynchronously queues a User message bubble on the e-Paper display.
func ShowUserMessage(rawText string) {
	clean := SanitizeChatText(rawText)
	if clean == "" {
		return
	}
	enqueueTask(displayTask{role: RoleUser, text: clean})
}

// ShowAgentMessage formats and asynchronously queues an Agent message bubble on the e-Paper display.
func ShowAgentMessage(rawText string) {
	clean := SanitizeChatText(rawText)
	if clean == "" {
		return
	}
	enqueueTask(displayTask{role: RoleAgent, text: clean})
}

// ShowAgentResponse is an alias for ShowAgentMessage for backwards compatibility.
func ShowAgentResponse(rawText string) {
	ShowAgentMessage(rawText)
}

// Close gracefully terminates the display background worker.
func Close() {
	workerMu.Lock()
	if isWorkerRunning {
		close(stopChan)
		isWorkerRunning = false
	}
	workerMu.Unlock()
	workerDone.Wait()
}

// runDisplayWorker processes display tasks sequentially to avoid overlapping USB transactions.
func runDisplayWorker() {
	defer workerDone.Done()

	var notFoundLogged bool

	for {
		select {
		case <-stopChan:
			return
		case task, ok := <-taskChan:
			if !ok {
				return
			}

			if !IsEnabled() {
				continue
			}

			// 1. Check for ESP32 e-Paper send_chat binary
			bin := findSendChat()
			if bin != "" {
				var cmdArg string
				if task.role == RoleUser {
					cmdArg = "/user " + task.text
				} else {
					cmdArg = "/agent " + task.text
				}

				if err := executeSendChat(bin, cmdArg); err != nil {
					log.Printf("[Display] Failed to send chat message to e-Paper: %v", err)
				} else {
					log.Printf("[Display] Updated e-Paper with %s message (%d chars)", task.role, len(task.text))
				}
				continue
			}

			// 2. Fallback to legacy LCD if send_chat is absent
			lcdBin := findLcdCtl()
			if lcdBin != "" {
				if task.role == RoleAgent {
					renderLegacyLcdAgentResponse(lcdBin, task.text)
				}
				continue
			}

			if !notFoundLogged {
				log.Println("[Display] Notice: Neither send_chat nor lcd_ctl found on system. Display updates will be skipped.")
				notFoundLogged = true
			}
		}
	}
}

// executeSendChat invokes the send_chat command on OpenStick Linux with a timeout.
func executeSendChat(binPath, cmdArg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, binPath, cmdArg)
	} else {
		cmd = exec.CommandContext(ctx, binPath, cmdArg)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Try with sudo -n if direct execution failed with permissions
		if os.Geteuid() != 0 {
			sudoCmd := exec.CommandContext(ctx, "sudo", "-n", binPath, cmdArg)
			if sudoOut, sudoErr := sudoCmd.CombinedOutput(); sudoErr == nil {
				return nil
			} else {
				return fmt.Errorf("%w (output: %s)", sudoErr, strings.TrimSpace(string(sudoOut)))
			}
		}
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SanitizeChatText strips markdown artifacts and normalizes newlines and whitespace for single-line CDC transmission.
func SanitizeChatText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\\n", " ")

	// Strip common markdown characters
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "#", "")

	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

// renderLegacyLcdAgentResponse handles legacy ST7735 LCD yellow-letter rendering.
func renderLegacyLcdAgentResponse(binPath, text string) {
	pairs := FormatWordPairs(text)
	if len(pairs) == 0 {
		return
	}

	for idx, pair := range pairs {
		if err := executeLcdText(binPath, pair.Text, pair.Size); err != nil {
			log.Printf("[Display] Failed to update LCD display: %v", err)
			return
		}

		dwell := WordPairDwellTime
		if idx == len(pairs)-1 {
			dwell = FinalPairDwellTime
		}
		time.Sleep(dwell)
	}

	_ = showGlososLogo(binPath)
}

// showGlososLogo displays the Glosos waveform logo on the legacy LCD.
func showGlososLogo(binPath string) error {
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binPath, "show", "wave")
	} else {
		cmd = exec.Command("sudo", "-n", binPath, "show", "wave")
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		fallbackCmd := exec.Command(binPath, "show", "wave")
		if _, fallbackErr := fallbackCmd.CombinedOutput(); fallbackErr == nil {
			return nil
		}
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// executeLcdText invokes lcd_ctl to print text with yellow letters on black background.
func executeLcdText(binPath, pageText string, size int) error {
	sizeStr := strconv.Itoa(size)
	args := []string{"text", "-color", "yellow", "-bg", "black", "-center", "-size", sizeStr, "-no-init", "-no-screenshot", pageText}

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binPath, args...)
	} else {
		cmd = exec.Command("sudo", append([]string{"-n", binPath}, args...)...)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		fallbackCmd := exec.Command(binPath, args...)
		if _, fallbackErr := fallbackCmd.CombinedOutput(); fallbackErr == nil {
			return nil
		}
		return fmt.Errorf("%w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FormatWordPairs breaks text into 2-word pairs (one under the other), choosing size 2 (big font)
// if both words fit within the 128px screen width (<= 8 runes), or size 1 for longer words.
func FormatWordPairs(text string) []WordPair {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\\n", "\n")

	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "#", "")

	rawWords := strings.Fields(text)
	var words []string
	for _, w := range rawWords {
		trimmed := strings.Trim(w, " \t\r\n")
		if trimmed == "" || trimmed == "-" || trimmed == "--" || trimmed == "•" {
			continue
		}
		words = append(words, trimmed)
	}

	if len(words) == 0 {
		return nil
	}

	var pairs []WordPair
	for i := 0; i < len(words); i += 2 {
		w1 := words[i]
		if i+1 < len(words) {
			w2 := words[i+1]
			pairText := w1 + "\n" + w2
			size := 2
			if len([]rune(w1)) > MaxRunesForBigFont || len([]rune(w2)) > MaxRunesForBigFont {
				size = 1
			}
			pairs = append(pairs, WordPair{Text: pairText, Size: size})
		} else {
			size := 2
			if len([]rune(w1)) > MaxRunesForBigFont {
				size = 1
			}
			pairs = append(pairs, WordPair{Text: w1, Size: size})
		}
	}

	return pairs
}

// formatTextPages wraps text into lines of at most maxCharsPerLine and chunks into pages of maxLinesPerPage.
func formatTextPages(text string, maxCharsPerLine, maxLinesPerPage int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\\n", "\n")

	rawLines := strings.Split(text, "\n")
	var wrappedLines []string

	for _, rawLine := range rawLines {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			if len(wrappedLines) > 0 && wrappedLines[len(wrappedLines)-1] != "" {
				wrappedLines = append(wrappedLines, "")
			}
			continue
		}

		words := strings.Fields(rawLine)
		currentLine := ""

		for _, word := range words {
			runes := []rune(word)
			for len(runes) > 0 {
				if currentLine == "" {
					if len(runes) <= maxCharsPerLine {
						currentLine = string(runes)
						break
					} else {
						wrappedLines = append(wrappedLines, string(runes[:maxCharsPerLine]))
						runes = runes[maxCharsPerLine:]
					}
				} else {
					if len([]rune(currentLine))+1+len(runes) <= maxCharsPerLine {
						currentLine += " " + string(runes)
						break
					} else {
						wrappedLines = append(wrappedLines, currentLine)
						currentLine = ""
					}
				}
			}
		}

		if currentLine != "" {
			wrappedLines = append(wrappedLines, currentLine)
		}
	}

	if len(wrappedLines) == 0 {
		return nil
	}

	var pages []string
	for i := 0; i < len(wrappedLines); i += maxLinesPerPage {
		end := i + maxLinesPerPage
		if end > len(wrappedLines) {
			end = len(wrappedLines)
		}
		page := strings.Join(wrappedLines[i:end], "\n")
		pages = append(pages, page)
	}

	return pages
}
