package usage

import (
	"math"
	"testing"
)

// sowExampleJSON mirrors the USAGE_PRICE_TABLE_JSON example from the SOW.
const sowExampleJSON = `{
  "claude": {
    "claude-sonnet-4-6": {
      "input_usd_per_mtok":        3.00,
      "output_usd_per_mtok":       15.00,
      "cache_write_usd_per_mtok":  3.75,
      "cache_read_usd_per_mtok":   0.30
    },
    "claude-haiku-4-5-20251001": {
      "input_usd_per_mtok":        0.80,
      "output_usd_per_mtok":       4.00,
      "cache_write_usd_per_mtok":  1.00,
      "cache_read_usd_per_mtok":   0.08
    }
  },
  "openai_tts": {
    "gpt-4o-mini-tts": { "usd_per_mchar": 15.00 },
    "tts-1":           { "usd_per_mchar": 15.00 }
  }
}`

func approx(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestLoadPriceTable_ParsesSOWExample(t *testing.T) {
	pt, err := LoadPriceTable(sowExampleJSON)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sonnet, ok := pt.Claude["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("missing claude-sonnet-4-6")
	}
	if sonnet.InputPerMTok != 3.00 || sonnet.OutputPerMTok != 15.00 ||
		sonnet.CacheWritePerMTok != 3.75 || sonnet.CacheReadPerMTok != 0.30 {
		t.Fatalf("sonnet rates: %+v", sonnet)
	}
	tts, ok := pt.OpenAITTS["gpt-4o-mini-tts"]
	if !ok || tts.PerMChar != 15.00 {
		t.Fatalf("tts rates: %+v ok=%v", tts, ok)
	}
}

func TestLoadPriceTable_EmptyYieldsEmptyTable(t *testing.T) {
	pt, err := LoadPriceTable("")
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(pt.Claude) != 0 || len(pt.OpenAITTS) != 0 {
		t.Fatalf("expected empty table, got %+v", pt)
	}
	// Unknown lookups must not panic on the nil-safe maps.
	if c := pt.ClaudeCostUSD("x", 1, 1, 1, 1); c != 0 {
		t.Fatalf("empty table claude cost: got %v want 0", c)
	}
	if c := pt.TTSCostUSD("x", 100); c != 0 {
		t.Fatalf("empty table tts cost: got %v want 0", c)
	}
}

func TestLoadPriceTable_InvalidJSONErrors(t *testing.T) {
	if _, err := LoadPriceTable("{not json"); err == nil {
		t.Fatal("expected error on invalid json")
	}
}

func TestClaudeCostUSD_AllTokenKinds(t *testing.T) {
	pt, _ := LoadPriceTable(sowExampleJSON)
	// 1M input, 1M output, 1M cache-write, 1M cache-read on sonnet.
	got := pt.ClaudeCostUSD("claude-sonnet-4-6", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	want := 3.00 + 15.00 + 3.75 + 0.30
	if !approx(got, want) {
		t.Fatalf("claude cost: got %v want %v", got, want)
	}
}

func TestTTSCostUSD_Known(t *testing.T) {
	pt, _ := LoadPriceTable(sowExampleJSON)
	// 200k chars at $15/Mchar = 3.00.
	got := pt.TTSCostUSD("tts-1", 200_000)
	if !approx(got, 3.00) {
		t.Fatalf("tts cost: got %v want 3.00", got)
	}
}

func TestCost_UnknownModelReturnsZero(t *testing.T) {
	pt, _ := LoadPriceTable(sowExampleJSON)
	if c := pt.ClaudeCostUSD("claude-unknown", 1_000_000, 1_000_000, 0, 0); c != 0 {
		t.Fatalf("unknown claude model: got %v want 0", c)
	}
	if c := pt.TTSCostUSD("tts-unknown", 1_000_000); c != 0 {
		t.Fatalf("unknown tts model: got %v want 0", c)
	}
}
