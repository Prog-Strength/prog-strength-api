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

func TestLoadPriceTable_EmptyYieldsDefaultTable(t *testing.T) {
	pt, err := LoadPriceTable("")
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	// Empty env means "use the hardcoded defaults" — the cap must work
	// without operator config so a forgotten env var can't silently
	// disable cost capping. Matching exact rates would make this test
	// brittle to legitimate price updates; assert structure instead.
	if _, ok := pt.Claude["claude-sonnet-4-6"]; !ok {
		t.Errorf("default table missing claude-sonnet-4-6")
	}
	if _, ok := pt.Claude["claude-haiku-4-5-20251001"]; !ok {
		t.Errorf("default table missing claude-haiku-4-5-20251001")
	}
	if _, ok := pt.OpenAITTS["gpt-4o-mini-tts"]; !ok {
		t.Errorf("default table missing gpt-4o-mini-tts")
	}
	// Unknown model still resolves to 0 (and a warning) — same posture
	// the cap relied on before this change.
	if c := pt.ClaudeCostUSD("claude-unknown", 1, 1, 1, 1); c != 0 {
		t.Errorf("default table unknown claude: got %v want 0", c)
	}
}

func TestDefaultPriceTable_RatesArePositive(t *testing.T) {
	pt := DefaultPriceTable()
	// Catches an accidental zero-fill on a refactor — a zeroed rate
	// would silently disable capping for that model. Cheap structural
	// check; exact values are deliberately not asserted here so a
	// legitimate price update doesn't trip the test.
	for model, r := range pt.Claude {
		if r.InputPerMTok <= 0 || r.OutputPerMTok <= 0 ||
			r.CacheWritePerMTok <= 0 || r.CacheReadPerMTok <= 0 {
			t.Errorf("claude %s has non-positive rate: %+v", model, r)
		}
	}
	for model, r := range pt.OpenAITTS {
		if r.PerMChar <= 0 {
			t.Errorf("openai_tts %s has non-positive rate: %+v", model, r)
		}
	}
}

func TestDefaultPriceTable_CoversAgentModelIDs(t *testing.T) {
	// The agent records exactly these strings to telemetry today. If
	// the agent flips model ids (e.g. a new Sonnet variant becomes the
	// default) and this test isn't updated alongside the price table,
	// the cap will silently undercount that model. The same drift would
	// surface in prod as a price_table_missing_model log line — this
	// test catches it at PR time instead.
	required := []string{
		"claude-sonnet-4-6",         // CLAUDE_MODEL_COMPLEX default
		"claude-haiku-4-5-20251001", // CLAUDE_MODEL_SIMPLE / _ROUTER default
		"gpt-4o-mini-tts",           // OPENAI_TTS_MODEL default
	}
	pt := DefaultPriceTable()
	for _, model := range required {
		_, inClaude := pt.Claude[model]
		_, inTTS := pt.OpenAITTS[model]
		if !inClaude && !inTTS {
			t.Errorf("default price table missing required model %q", model)
		}
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
