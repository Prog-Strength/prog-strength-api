package usage

import (
	"encoding/json"
	"log"
)

// ClaudeRates holds the per-million-token prices for one Claude model,
// split by token kind because cache writes/reads price differently from
// fresh input/output.
type ClaudeRates struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// TTSRates holds the per-million-character price for one OpenAI TTS model.
type TTSRates struct {
	PerMChar float64
}

// PriceTable maps model id -> rates for each metered surface. Keys are the
// exact model strings the agent records to agent_turns.model /
// agent_speak_calls.model; a drift between those and these keys surfaces
// as a price_table_missing_model warning at read time.
type PriceTable struct {
	Claude    map[string]ClaudeRates
	OpenAITTS map[string]TTSRates
}

// priceTableJSON is the wire shape of USAGE_PRICE_TABLE_JSON. Kept
// separate from the in-memory PriceTable so the env field names
// (..._usd_per_mtok / usd_per_mchar) stay an external contract rather
// than leaking into the Go type.
type priceTableJSON struct {
	Claude map[string]struct {
		InputUSDPerMTok      float64 `json:"input_usd_per_mtok"`
		OutputUSDPerMTok     float64 `json:"output_usd_per_mtok"`
		CacheWriteUSDPerMTok float64 `json:"cache_write_usd_per_mtok"`
		CacheReadUSDPerMTok  float64 `json:"cache_read_usd_per_mtok"`
	} `json:"claude"`
	OpenAITTS map[string]struct {
		USDPerMChar float64 `json:"usd_per_mchar"`
	} `json:"openai_tts"`
}

// LoadPriceTable parses the USAGE_PRICE_TABLE_JSON env value. An empty
// string yields an empty (non-nil) table with no error so a missing env
// var degrades to "everything costs 0" rather than failing startup.
func LoadPriceTable(jsonStr string) (PriceTable, error) {
	pt := PriceTable{
		Claude:    map[string]ClaudeRates{},
		OpenAITTS: map[string]TTSRates{},
	}
	if jsonStr == "" {
		return pt, nil
	}

	var raw priceTableJSON
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return PriceTable{}, err
	}

	for model, r := range raw.Claude {
		pt.Claude[model] = ClaudeRates{
			InputPerMTok:      r.InputUSDPerMTok,
			OutputPerMTok:     r.OutputUSDPerMTok,
			CacheWritePerMTok: r.CacheWriteUSDPerMTok,
			CacheReadPerMTok:  r.CacheReadUSDPerMTok,
		}
	}
	for model, r := range raw.OpenAITTS {
		pt.OpenAITTS[model] = TTSRates{PerMChar: r.USDPerMChar}
	}
	return pt, nil
}

const perMillion = 1_000_000.0

// ClaudeCostUSD prices one model's token totals. An unknown model logs a
// price_table_missing_model warning and contributes 0 — the cap bar
// undercounts rather than 500ing on a price/model drift.
func (pt PriceTable) ClaudeCostUSD(model string, in, out, cacheCreate, cacheRead int64) float64 {
	rates, ok := pt.Claude[model]
	if !ok {
		log.Printf("price_table_missing_model: surface=claude model=%q", model)
		return 0
	}
	return float64(in)/perMillion*rates.InputPerMTok +
		float64(out)/perMillion*rates.OutputPerMTok +
		float64(cacheCreate)/perMillion*rates.CacheWritePerMTok +
		float64(cacheRead)/perMillion*rates.CacheReadPerMTok
}

// TTSCostUSD prices one TTS model's character total. Unknown model →
// 0 + warning, same posture as ClaudeCostUSD.
func (pt PriceTable) TTSCostUSD(model string, chars int64) float64 {
	rates, ok := pt.OpenAITTS[model]
	if !ok {
		log.Printf("price_table_missing_model: surface=openai_tts model=%q", model)
		return 0
	}
	return float64(chars) / perMillion * rates.PerMChar
}
