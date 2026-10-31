package sim

import "archsim/engine/catalog"

type Verdict string

const (
	VerdictSafe     Verdict = "safe"
	VerdictWarning  Verdict = "warning"
	VerdictOverload Verdict = "overload"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Issue struct {
	Severity Severity `json:"severity"`
	Ref      string   `json:"ref"` // node or edge id
	Message  string   `json:"message"`
}

// Report is the steady-state evaluation at one TPS value.
type Report struct {
	TPS     float64  `json:"tps"`
	Verdict Verdict  `json:"verdict"`
	Reasons []string `json:"reasons"`
	// Stable is false when some station receives more than its capacity; the
	// queue then grows without bound and latency/error figures are omitted.
	Stable      bool             `json:"stable"`
	Latency     *LatencyStats    `json:"latency,omitempty"`
	ErrorRate   *float64         `json:"errorRate,omitempty"`
	Resources   []ResourceReport `json:"resources"`
	Hops        []HopReport      `json:"hops"`
	Async       []AsyncReport    `json:"async,omitempty"`
	Replicas    []ReplicaReport  `json:"replicas,omitempty"`
	NodePools   []PoolReport     `json:"nodePools,omitempty"`
	Issues      []Issue          `json:"issues,omitempty"`
	Confidence  Confidence       `json:"confidence"`
	Assumptions []Assumption     `json:"assumptions"`

	feasible  bool
	limitedBy string
}

type LatencyStats struct {
	MeanMs float64 `json:"meanMs"`
	P50Ms  float64 `json:"p50Ms"`
	P95Ms  float64 `json:"p95Ms"`
	P99Ms  float64 `json:"p99Ms"`
}

// ResourceReport is one capacity-limited resource. Utilization is nil when the
// resource has no capacity at all (e.g. zero replicas scheduled).
type ResourceReport struct {
	ID          string   `json:"id"`
	Node        string   `json:"node"`
	Kind        string   `json:"kind"`
	Utilization *float64 `json:"utilization"`
	Load        float64  `json:"load"`
	Capacity    float64  `json:"capacity"`
	Unit        string   `json:"unit"`
	// ScalingHeadroom means an autoscaler can still add capacity, so only the
	// overload threshold (not the warning one) applies.
	ScalingHeadroom bool   `json:"scalingHeadroom,omitempty"`
	Note            string `json:"note,omitempty"`

	critical []string
}

// HopReport is the mean contribution of one node to a request (averaged over
// all requests, including those that do not visit it).
type HopReport struct {
	Node             string  `json:"node"`
	VisitsPerRequest float64 `json:"visitsPerRequest"`
	NetworkMs        float64 `json:"networkMs"`
	WaitMs           float64 `json:"waitMs"`
	ServiceMs        float64 `json:"serviceMs"`
	DelayMs          float64 `json:"delayMs"`
}

// AsyncReport is the processing latency downstream of an async edge
// (excluding time spent waiting in the queue/stream itself).
type AsyncReport struct {
	Edge  string  `json:"edge"`
	P50Ms float64 `json:"p50Ms"`
	P99Ms float64 `json:"p99Ms"`
}

type ReplicaReport struct {
	Service    string `json:"service"`
	Host       string `json:"host,omitempty"`
	Autoscaler string `json:"autoscaler,omitempty"`
	Min        int    `json:"min,omitempty"`
	Max        int    `json:"max,omitempty"`
	Desired    int    `json:"desired"`
	Scheduled  int    `json:"scheduled"`
	Pending    int    `json:"pending,omitempty"`
	// Consuming is the replicas that actually consume when limited by partitions.
	Consuming int `json:"consuming,omitempty"`
}

type PoolReport struct {
	Node              string  `json:"node"`
	NodesNeeded       int     `json:"nodesNeeded"`
	NodesAvailable    int     `json:"nodesAvailable"`
	PendingPods       int     `json:"pendingPods,omitempty"`
	AllocatableCPU    float64 `json:"allocatableCpuPerNode"`
	AllocatableMemGiB float64 `json:"allocatableMemGiBPerNode"`
}

type Confidence struct {
	Score            float64  `json:"score"` // 0..1
	Level            string   `json:"level"` // low | medium | high
	CriticalDefaults []string `json:"criticalDefaults,omitempty"`
	Summary          string   `json:"summary"`
}

// Result is a full quick check: the report at the target TPS plus the
// maximum sustainable TPS.
type Result struct {
	CatalogVersion string `json:"catalogVersion"`
	Target         Report `json:"target"`
	// MaxSustainableTPS is nil when no resource bounds throughput.
	MaxSustainableTPS *float64 `json:"maxSustainableTps"`
	LimitedBy         string   `json:"limitedBy"`
	// SaturationOrder lists resources by utilization at the max sustainable TPS.
	SaturationOrder []ResourceReport `json:"saturationOrder"`
}

var sourceScore = map[catalog.SourceKind]float64{
	catalog.SourceUser:            1.0,
	catalog.SourceVendorDocs:      0.9,
	catalog.SourceVendorBenchmark: 0.6,
	catalog.SourceDefaultEstimate: 0.3,
}
