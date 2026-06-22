package vectormemory

import "github.com/prometheus/client_golang/prometheus"

// Prometheus series for the background distillation goroutine — the data
// behind the "ps-vector-memory" Grafana dashboard (prog-strength-infra
// monitoring/grafana/dashboards/ps-vector-memory.json). The goroutine
// degrades silently: if it stops running, or runs but writes nothing, no
// request fails and nothing pages. These collectors are stamped at the
// decision points job.go and service.go already log, so the dashboard shows
// the aggregate and CloudWatch shows any single sweep's story.
//
// Cardinality: every label is a small closed set (three sweep results, seven
// pipeline stages, two token kinds). Safe at any volume — one tick every five
// minutes regardless.

// lastSweepTimestamp is stamped (Unix seconds) at the end of EVERY sweep
// attempt, success or batch error. It answers "is the loop executing at all?":
// a value that stops advancing means the ticker goroutine died.
var lastSweepTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_vectormemory_last_sweep_timestamp_seconds",
	Help: "Unix timestamp of the end of the most recent sweep attempt (success or batch error).",
})

// lastSuccessTimestamp is stamped only when a sweep completes without a
// batch-level (select) error. It answers "is the loop executing AND
// completing?" — the dashboard's headline tile derives from it.
var lastSuccessTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_vectormemory_last_success_timestamp_seconds",
	Help: "Unix timestamp of the most recent sweep that completed without a batch-level error.",
})

// idleSessions is the full count of idle, undistilled sessions at the start of
// each sweep — the true backlog, visible even when a single batch caps at
// distillBatchSize.
var idleSessions = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "api_vectormemory_idle_sessions",
	Help: "Idle, undistilled chat sessions awaiting distillation, sampled at the start of each sweep.",
})

// sweepsTotal counts ticks by result:
//
//	error   — batch select failed; nothing was processed
//	partial — sweep completed but at least one session hit a stage error
//	success — every selected session was distilled (including zero selected)
var sweepsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_vectormemory_sweeps_total",
		Help: "Distillation sweeps by result (success/partial/error).",
	},
	[]string{"result"},
)

// sessionsSelectedTotal counts idle sessions pulled into a batch.
var sessionsSelectedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_vectormemory_sessions_selected_total",
	Help: "Idle sessions pulled into a distillation batch.",
})

// sessionsDistilledTotal counts sessions successfully processed and marked.
var sessionsDistilledTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_vectormemory_sessions_distilled_total",
	Help: "Sessions successfully distilled and marked.",
})

// observationsDistilledTotal counts raw observations returned by the distiller.
var observationsDistilledTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_vectormemory_observations_distilled_total",
	Help: "Raw observations returned by the distiller.",
})

// observationsInsertedTotal counts observations actually written to
// agent_memories / vec_agent_memories.
var observationsInsertedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_vectormemory_observations_inserted_total",
	Help: "Observations written to the memory store.",
})

// observationsDedupedTotal counts observations skipped as near-duplicates
// (distance <= dedup_threshold). The gap between distilled and
// inserted+deduped surfaces silent per-observation insert loss.
var observationsDedupedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_vectormemory_observations_deduped_total",
	Help: "Observations skipped as near-duplicates of an existing memory.",
})

// stageErrorsTotal counts failures at each point in the pipeline, mapped to
// the existing WARN/ERROR sites: select | load | distill | embed | dedup |
// insert | mark. The label points straight at the failing dependency.
var stageErrorsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_vectormemory_stage_errors_total",
		Help: "Pipeline failures by stage (select/load/distill/embed/dedup/insert/mark).",
	},
	[]string{"stage"},
)

// sweepDuration records wall time for a full tick. Buckets are weighted toward
// seconds–minutes; a sweep approaching the five-minute tick interval is a
// problem.
var sweepDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "api_vectormemory_sweep_duration_seconds",
	Help:    "Wall time of a full distillation sweep, in seconds.",
	Buckets: []float64{1, 2.5, 5, 10, 30, 60, 120},
})

// distillDuration records Anthropic distill-call latency.
var distillDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "api_vectormemory_distill_duration_seconds",
	Help:    "Anthropic distill call latency, in seconds.",
	Buckets: []float64{0.25, 0.5, 1, 2, 4, 8, 16, 32},
})

// embedDuration records OpenAI embed-call latency.
var embedDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "api_vectormemory_embed_duration_seconds",
	Help:    "OpenAI embed call latency, in seconds.",
	Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
})

// distillTokensTotal counts Anthropic token spend by kind (input | output),
// from the response usage block. A spend climb without a matching
// observations_inserted climb is the signature of a failing-and-retrying loop.
var distillTokensTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "api_vectormemory_distill_tokens_total",
		Help: "Anthropic distill token usage by kind (input/output).",
	},
	[]string{"kind"},
)

// embedTokensTotal counts OpenAI embedding token spend (usage.total_tokens).
var embedTokensTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_vectormemory_embed_tokens_total",
	Help: "OpenAI embedding token usage (total tokens).",
})

func init() {
	prometheus.MustRegister(
		lastSweepTimestamp,
		lastSuccessTimestamp,
		idleSessions,
		sweepsTotal,
		sessionsSelectedTotal,
		sessionsDistilledTotal,
		observationsDistilledTotal,
		observationsInsertedTotal,
		observationsDedupedTotal,
		stageErrorsTotal,
		sweepDuration,
		distillDuration,
		embedDuration,
		distillTokensTotal,
		embedTokensTotal,
	)
}
