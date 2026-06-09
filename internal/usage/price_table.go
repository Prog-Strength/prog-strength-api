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

// DefaultPriceTable returns the price map shipped in source. These rates
// are public reference data (anthropic.com/pricing, openai.com/api/pricing)
// — they live here, not in a secret, so a price change is a reviewable
// diff in git history rather than an opaque secret rotation. Keys are the
// exact model strings the agent records to agent_turns.model /
// agent_speak_calls.model; keep them in sync when the agent's model
// config changes.
//
// Last verified: 2026-06-09. Update the table and bump this date when
// the pricing pages change.
func DefaultPriceTable() PriceTable {
	return PriceTable{
		Claude: map[string]ClaudeRates{
			// Sonnet 4.6 — anthropic.com/pricing
			"claude-sonnet-4-6": {
				InputPerMTok:      3.00,
				OutputPerMTok:     15.00,
				CacheWritePerMTok: 3.75, // 5-minute cache; 1-hour rate is higher
				CacheReadPerMTok:  0.30,
			},
			// Haiku 4.5 — anthropic.com/pricing
			"claude-haiku-4-5-20251001": {
				InputPerMTok:      1.00,
				OutputPerMTok:     5.00,
				CacheWritePerMTok: 1.25,
				CacheReadPerMTok:  0.10,
			},
		},
		OpenAITTS: map[string]TTSRates{
			// gpt-4o-mini-tts — openai.com/api/pricing. OpenAI bills this
			// model by audio tokens, not characters; $12/Mchar is a
			// midpoint estimate from their ~$0.015/min published rate
			// at typical English speaking pace. Overshoots real cost by
			// ~20% in the worst case — safe direction for a cap.
			"gpt-4o-mini-tts": {PerMChar: 12.00},
			// tts-1 — documented rate. Listed so an OPENAI_TTS_MODEL
			// override doesn't silently fall through to 0 and bypass
			// the cap.
			"tts-1": {PerMChar: 15.00},
		},
	}
}

// LoadPriceTable returns the effective price table for the API. When
// jsonStr is empty, returns DefaultPriceTable so the cap works
// out-of-the-box with no env config. When jsonStr is non-empty, parses
// it as a full override — the env wholly replaces the default rather
// than merging, so an emergency override has predictable semantics.
// Invalid JSON is an error; callers can choose to log and fall back to
// the default.
func LoadPriceTable(jsonStr string) (PriceTable, error) {
	if jsonStr == "" {
		return DefaultPriceTable(), nil
	}

	var raw priceTableJSON
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return PriceTable{}, err
	}

	pt := PriceTable{
		Claude:    map[string]ClaudeRates{},
		OpenAITTS: map[string]TTSRates{},
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
