package display

import (
	"strings"
	"testing"
	"time"
)

func TestFormatTextPages(t *testing.T) {
	text := "Hello world, this is a test of formatting text for the small device LCD screen."
	pages := formatTextPages(text, 15, 5)
	if len(pages) == 0 {
		t.Fatalf("expected pages, got 0")
	}

	for _, page := range pages {
		lines := strings.Split(page, "\n")
		if len(lines) > 5 {
			t.Errorf("page has %d lines, expected max 5", len(lines))
		}
		for _, line := range lines {
			if len([]rune(line)) > 15 {
				t.Errorf("line %q exceeds 15 chars (len=%d)", line, len([]rune(line)))
			}
		}
	}
}

func TestFormatTextPagesLongWord(t *testing.T) {
	text := "Supercalifragilisticexpialidocious"
	pages := formatTextPages(text, 15, 5)
	if len(pages) == 0 {
		t.Fatalf("expected pages, got 0")
	}
	allLines := strings.Split(pages[0], "\n")
	for _, line := range allLines {
		if len([]rune(line)) > 15 {
			t.Errorf("line %q exceeds 15 chars", line)
		}
	}
}

func TestFormatTextPagesCyrillic(t *testing.T) {
	text := "Привет! Я твой голосовой ассистент на устройстве."
	pages := formatTextPages(text, 15, 11)
	if len(pages) == 0 {
		t.Fatalf("expected pages, got 0")
	}
	for _, page := range pages {
		for _, line := range strings.Split(page, "\n") {
			if len([]rune(line)) > 15 {
				t.Errorf("cyrillic line %q exceeds 15 chars (len=%d)", line, len([]rune(line)))
			}
		}
	}
}

func TestFormatWordPairs(t *testing.T) {
	text := "This application tracks your daily calorie intake."
	pairs := FormatWordPairs(text)

	if len(pairs) != 4 {
		t.Fatalf("expected 4 pairs, got %d", len(pairs))
	}

	// Pair 0: "This\napplication" (application is 11 chars > 8, so size 1)
	if pairs[0].Text != "This\napplication" || pairs[0].Size != 1 {
		t.Errorf("pair 0 mismatch: text=%q size=%d", pairs[0].Text, pairs[0].Size)
	}

	// Pair 1: "tracks\nyour" (6 and 4 chars <= 8, so size 2)
	if pairs[1].Text != "tracks\nyour" || pairs[1].Size != 2 {
		t.Errorf("pair 1 mismatch: text=%q size=%d", pairs[1].Text, pairs[1].Size)
	}

	// Pair 2: "daily\ncalorie" (5 and 7 chars <= 8, so size 2)
	if pairs[2].Text != "daily\ncalorie" || pairs[2].Size != 2 {
		t.Errorf("pair 2 mismatch: text=%q size=%d", pairs[2].Text, pairs[2].Size)
	}

	// Pair 3: "intake." (odd word, 7 chars <= 8, so size 2)
	if pairs[3].Text != "intake." || pairs[3].Size != 2 {
		t.Errorf("pair 3 mismatch: text=%q size=%d", pairs[3].Text, pairs[3].Size)
	}
}

func TestFormatWordPairsMarkdown(t *testing.T) {
	text := "**Opened** `Alina.`"
	pairs := FormatWordPairs(text)
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
	if pairs[0].Text != "Opened\nAlina." || pairs[0].Size != 2 {
		t.Errorf("markdown pair mismatch: text=%q size=%d", pairs[0].Text, pairs[0].Size)
	}
}

func TestFormatWordPairsCyrillic(t *testing.T) {
	text := "Привет мир"
	pairs := FormatWordPairs(text)
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
	if pairs[0].Text != "Привет\nмир" || pairs[0].Size != 2 {
		t.Errorf("cyrillic pair mismatch: text=%q size=%d", pairs[0].Text, pairs[0].Size)
	}
}

func TestLiveDisplayOnDevice(t *testing.T) {
	ShowAgentResponse("Agent response:\nAll systems active!")
	time.Sleep(500 * time.Millisecond)
}


