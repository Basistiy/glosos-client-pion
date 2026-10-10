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

// WordPair represents a single display screen showing 1 or 2 words centered, one under the other.
type WordPair struct {
	Text string
	Size int
}

var (
	displayMu    sync.Mutex
	activeCancel context.CancelFunc
	enabled      = true
	cachedBin    string
	binSearched  bool
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

// findLcdCtl locates the lcd_ctl binary on the system.
func findLcdCtl() string {
	if binSearched {
		return cachedBin
	}
	binSearched = true

	candidates := []string{
		"/usr/local/bin/lcd_ctl",
		"/usr/bin/lcd_ctl",
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			cachedBin = c
			return cachedBin
		}
	}

	if lp, err := exec.LookPath("lcd_ctl"); err == nil {
		cachedBin = lp
		return cachedBin
	}

	return ""
}

// ShowAgentResponse formats and displays the agent response on the device LCD in yellow letters.
// If multiple responses arrive rapidly, any existing paging is canceled and the newest response is shown.
func ShowAgentResponse(rawText string) {
	if !IsEnabled() {
		return
	}

	text := strings.TrimSpace(rawText)
	if text == "" {
		return
	}

	displayMu.Lock()
	// Cancel any active paging loop from previous agent message
	if activeCancel != nil {
		activeCancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	activeCancel = cancel
	displayMu.Unlock()

	go renderAgentResponseRoutine(ctx, text)
}

func renderAgentResponseRoutine(ctx context.Context, text string) {
	binPath := findLcdCtl()
	if binPath == "" {
		// Not running on an OpenStick/MF800 device or lcd_ctl not installed
		return
	}

	pairs := FormatWordPairs(text)
	if len(pairs) == 0 {
		return
	}

	for idx, pair := range pairs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := executeLcdText(binPath, pair.Text, pair.Size); err != nil {
			log.Printf("[Display] Failed to update display: %v", err)
			return
		}

		log.Printf("[Display] Printed agent response on LCD (pair %d/%d, size %d)", idx+1, len(pairs), pair.Size)

		// Dwell before showing next pair (or before showing loading logo after the last pair)
		dwell := WordPairDwellTime
		if idx == len(pairs)-1 {
			dwell = FinalPairDwellTime
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(dwell):
		}
	}

	// Show glosos loading logo after all text has been displayed
	select {
	case <-ctx.Done():
		return
	default:
	}

	if err := showGlososLogo(binPath); err != nil {
		log.Printf("[Display] Failed to restore glosos logo: %v", err)
	} else {
		log.Printf("[Display] Restored glosos loading logo on LCD")
	}
}

// showGlososLogo displays the Glosos waveform logo on the screen.
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

	// Try without sudo if running as root, otherwise try with sudo -n
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binPath, args...)
	} else {
		cmd = exec.Command("sudo", append([]string{"-n", binPath}, args...)...)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to direct invocation if sudo -n fails
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
	// Normalize line breaks
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\\n", "\n")

	// Strip markdown artifacts
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
	// Normalize literal \n or CRLF
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
						// Word is longer than an entire line; slice it
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
