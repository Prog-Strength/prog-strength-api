package nutritionlookup

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// macroSelection describes who picks the macros for a custom meal. The API
// never auto-selects a provider hit — it returns ranked candidates and the
// agent (or direct API client) chooses one. When lookup fails or returns no
// matches, the agent falls back to LLM estimation per the custom-meal prompt.
const (
	macroSelectionAgentChooses      = "agent_chooses_from_candidates"
	macroSelectionAgentMustEstimate = "agent_must_estimate"
)

func logLookupCandidates(ctx context.Context, log *slog.Logger, msg string, matches []Candidate, attrs ...any) {
	if len(matches) == 0 {
		return
	}
	args := append([]any{
		slog.Int("matches", len(matches)),
		slog.String("matches_summary", strings.Join(candidateSummaryLines(matches), " | ")),
	}, attrs...)
	log.InfoContext(ctx, msg, args...)

	if log.Enabled(ctx, slog.LevelDebug) {
		log.DebugContext(ctx, msg+" detail",
			append([]any{slog.Any("matches_detail", candidateDebugRows(matches))}, attrs...)...)
	}
}

func candidateSummaryLines(matches []Candidate) []string {
	lines := make([]string, 0, len(matches))
	for i, c := range matches {
		lines = append(lines, candidateSummaryLine(i+1, c))
	}
	return lines
}

func candidateSummaryLine(rank int, c Candidate) string {
	name := truncateLogField(c.Name, 48)
	brand := truncateLogField(c.Brand, 24)
	label := name
	if brand != "" {
		label = fmt.Sprintf("%s (%s)", name, brand)
	}
	flags := candidateFlags(c)
	flagSuffix := ""
	if flags != "" {
		flagSuffix = " [" + flags + "]"
	}
	return fmt.Sprintf("#%d %s/%s %s %gkcal/serving%s",
		rank, c.Source, c.SourceID, label, c.PerServing.Calories, flagSuffix)
}

func candidateFlags(c Candidate) string {
	var flags []string
	if c.PlausibilityWarning != "" {
		flags = append(flags, "plausibility_warning")
	}
	if c.Stale {
		flags = append(flags, "stale")
	}
	return strings.Join(flags, ",")
}

func candidateDebugRows(matches []Candidate) []string {
	rows := make([]string, 0, len(matches))
	for i, c := range matches {
		rows = append(rows, fmt.Sprintf(
			"#%d source=%s id=%s name=%q brand=%q serving=%q per_serving={cal:%g p:%g f:%g c:%g} total_for_quantity={cal:%g p:%g f:%g c:%g} plausibility_warning=%t stale=%t",
			i+1,
			c.Source,
			c.SourceID,
			c.Name,
			c.Brand,
			c.ServingDescription,
			c.PerServing.Calories, c.PerServing.ProteinG, c.PerServing.FatG, c.PerServing.CarbsG,
			c.TotalForQuantity.Calories, c.TotalForQuantity.ProteinG, c.TotalForQuantity.FatG, c.TotalForQuantity.CarbsG,
			c.PlausibilityWarning != "",
			c.Stale,
		))
	}
	return rows
}

func truncateLogField(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}
