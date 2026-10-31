// Package model defines the architecture document (schema v0.1) that every
// engine mode consumes. It holds no behaviour beyond parsing and structural
// validation; component semantics live in the catalog and the engines.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const SchemaVersion = "0.1"

type Architecture struct {
	Version        string   `json:"version"`
	CatalogVersion string   `json:"catalogVersion"`
	Nodes          []Node   `json:"nodes"`
	Edges          []Edge   `json:"edges"`
	Workload       Workload `json:"workload"`
	// UI is editor state (canvas positions, sizes). The engines ignore it;
	// it lives here so a design stays one self-contained file.
	UI json.RawMessage `json:"ui,omitempty"`
}

// Node is one component on the canvas. Config keys are catalog parameters;
// anything not set falls back to the catalog default (and is reported as an
// assumption with its source).
type Node struct {
	ID     string         `json:"id"`
	Kind   string         `json:"kind"`
	Name   string         `json:"name,omitempty"`
	Parent string         `json:"parent,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

// Edge is one communication path from caller (From) to callee (To).
type Edge struct {
	ID           string   `json:"id,omitempty"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	Protocol     string   `json:"protocol,omitempty"`     // defaults to the callee's catalog protocol
	NetworkClass string   `json:"networkClass,omitempty"` // same-host | same-az | cross-az | cross-region | internet
	LatencyMs    *float64 `json:"latencyMs,omitempty"`    // overrides the network class RTT
	Op           string   `json:"op,omitempty"`           // datastore operation: read | write | complex
	// Classes limits the edge to these request classes; empty means all.
	Classes         []string `json:"classes,omitempty"`
	CallsPerRequest *float64 `json:"callsPerRequest,omitempty"`
	Probability     *float64 `json:"probability,omitempty"`
	// When derives the probability from a cache node's hit ratio:
	// "cache_miss:<cacheNodeId>" or "cache_hit:<cacheNodeId>".
	When string `json:"when,omitempty"`
	// Edges from the same caller sharing a ParallelGroup run concurrently;
	// all other calls run sequentially in edge order.
	ParallelGroup string   `json:"parallelGroup,omitempty"`
	Async         bool     `json:"async,omitempty"` // caller does not wait for the callee
	PayloadReqKB  *float64 `json:"payloadReqKB,omitempty"`
	PayloadRespKB *float64 `json:"payloadRespKB,omitempty"`
	PoolSize      *int     `json:"poolSize,omitempty"` // connections per caller replica
	KeepAlive     *bool    `json:"keepAlive,omitempty"`
	TimeoutMs     float64  `json:"timeoutMs,omitempty"`
	Retry         *Retry   `json:"retry,omitempty"`
}

type Retry struct {
	Max       int     `json:"max"`
	BackoffMs float64 `json:"backoffMs"`
}

type Workload struct {
	Entry      string         `json:"entry"`
	TargetTPS  float64        `json:"targetTps"`
	Classes    []RequestClass `json:"classes,omitempty"`
	SLO        *SLO           `json:"slo,omitempty"`
	Thresholds *Thresholds    `json:"thresholds,omitempty"`
	Seed       uint64         `json:"seed,omitempty"`
	Samples    int            `json:"samples,omitempty"`
	Timeline   *Timeline      `json:"timeline,omitempty"`
}

// Timeline configures the time simulation. The load starts at TargetTPS.
type Timeline struct {
	DurationS int          `json:"durationS,omitempty"`
	Load      *LoadPattern `json:"load,omitempty"` // nil = constant TargetTPS
	// QueueTimeoutMs drops queued work that has waited longer than this
	// (clients give up), counting it as errors.
	QueueTimeoutMs float64 `json:"queueTimeoutMs,omitempty"`
	SamplesPerTick int     `json:"samplesPerTick,omitempty"`
}

// LoadPattern shapes offered load over time.
//
//	constant: TargetTPS throughout
//	spike:    TargetTPS, then a linear ramp of RampS to PeakTPS at StartS,
//	          held for HoldS, then a linear ramp back down
type LoadPattern struct {
	Kind    string  `json:"kind"`
	PeakTPS float64 `json:"peakTps,omitempty"`
	StartS  float64 `json:"startS,omitempty"`
	RampS   float64 `json:"rampS,omitempty"`
	HoldS   float64 `json:"holdS,omitempty"`
}

const (
	LoadConstant = "constant"
	LoadSpike    = "spike"
)

type RequestClass struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

type SLO struct {
	P99Ms     float64  `json:"p99Ms,omitempty"`
	ErrorRate *float64 `json:"errorRate,omitempty"`
}

// Thresholds are utilization levels (0..1) for the warning and overload verdicts.
type Thresholds struct {
	Warn     float64 `json:"warn"`
	Overload float64 `json:"overload"`
}

const (
	DefaultWarn      = 0.70
	DefaultOverload  = 0.85
	DefaultErrorRate = 0.001
	DefaultSamples   = 20000
	DefaultSeed      = 1
	DefaultClass     = "default"

	DefaultDurationS      = 600
	MaxDurationS          = 3600
	DefaultQueueTimeoutMs = 30000
	DefaultSamplesPerTick = 1000
	MaxSamplesPerTick     = 20000
)

// Parse decodes an architecture document, rejecting unknown fields so typos
// surface as errors instead of silently ignored settings.
func Parse(data []byte) (*Architecture, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var a Architecture
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("parse architecture: %w", err)
	}
	return &a, nil
}

// RequestClasses returns the workload classes, or a single default class.
func (w Workload) RequestClasses() []RequestClass {
	if len(w.Classes) == 0 {
		return []RequestClass{{Name: DefaultClass, Weight: 1}}
	}
	return w.Classes
}

func (w Workload) Limits() Thresholds {
	t := Thresholds{Warn: DefaultWarn, Overload: DefaultOverload}
	if w.Thresholds != nil {
		t = *w.Thresholds
	}
	return t
}

func (w Workload) ErrorBudget() float64 {
	if w.SLO != nil && w.SLO.ErrorRate != nil {
		return *w.SLO.ErrorRate
	}
	return DefaultErrorRate
}

func (w Workload) SampleCount() int {
	if w.Samples > 0 {
		return w.Samples
	}
	return DefaultSamples
}

func (w Workload) RandSeed() uint64 {
	if w.Seed != 0 {
		return w.Seed
	}
	return DefaultSeed
}

func (t *Timeline) Duration() int {
	if t != nil && t.DurationS > 0 {
		return t.DurationS
	}
	return DefaultDurationS
}

func (t *Timeline) QueueTimeout() float64 {
	if t != nil && t.QueueTimeoutMs > 0 {
		return t.QueueTimeoutMs
	}
	return DefaultQueueTimeoutMs
}

func (t *Timeline) TickSamples() int {
	if t != nil && t.SamplesPerTick > 0 {
		return t.SamplesPerTick
	}
	return DefaultSamplesPerTick
}

// TPSAt returns the offered load at time t seconds.
func (w Workload) TPSAt(t float64) float64 {
	base := w.TargetTPS
	if w.Timeline == nil || w.Timeline.Load == nil || w.Timeline.Load.Kind != LoadSpike {
		return base
	}
	p := w.Timeline.Load
	up := p.StartS + p.RampS
	down := up + p.HoldS
	switch {
	case t < p.StartS || t >= down+p.RampS:
		return base
	case t < up:
		return base + (p.PeakTPS-base)*(t-p.StartS)/p.RampS
	case t < down:
		return p.PeakTPS
	default:
		return p.PeakTPS + (base-p.PeakTPS)*(t-down)/p.RampS
	}
}

// Validate checks structure that does not need the catalog: identifiers,
// references and value ranges. All problems are reported together.
func (a *Architecture) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if a.Version != SchemaVersion {
		add("version %q is not supported (expected %q)", a.Version, SchemaVersion)
	}

	ids := make(map[string]bool, len(a.Nodes))
	for _, n := range a.Nodes {
		switch {
		case n.ID == "":
			add("node with kind %q has an empty id", n.Kind)
		case ids[n.ID]:
			add("duplicate node id %q", n.ID)
		}
		ids[n.ID] = true
		if n.Kind == "" {
			add("node %q has an empty kind", n.ID)
		}
	}
	for _, n := range a.Nodes {
		if n.Parent != "" && !ids[n.Parent] {
			add("node %q: parent %q does not exist", n.ID, n.Parent)
		}
		if n.Parent == n.ID && n.ID != "" {
			add("node %q is its own parent", n.ID)
		}
	}

	classes := make(map[string]bool)
	for _, c := range a.Workload.RequestClasses() {
		if c.Name == "" {
			add("workload class with empty name")
		}
		if classes[c.Name] {
			add("duplicate workload class %q", c.Name)
		}
		classes[c.Name] = true
		if !(c.Weight > 0) {
			add("workload class %q: weight must be > 0", c.Name)
		}
	}

	edgeIDs := make(map[string]bool, len(a.Edges))
	for i, e := range a.Edges {
		ref := fmt.Sprintf("edge %d (%s->%s)", i, e.From, e.To)
		if e.ID != "" {
			if edgeIDs[e.ID] {
				add("duplicate edge id %q", e.ID)
			}
			edgeIDs[e.ID] = true
		}
		if !ids[e.From] {
			add("%s: from %q does not exist", ref, e.From)
		}
		if !ids[e.To] {
			add("%s: to %q does not exist", ref, e.To)
		}
		if e.From == e.To {
			add("%s: self-loop is not allowed", ref)
		}
		if e.CallsPerRequest != nil && *e.CallsPerRequest < 0 {
			add("%s: callsPerRequest must be >= 0", ref)
		}
		if e.Probability != nil && (*e.Probability < 0 || *e.Probability > 1) {
			add("%s: probability must be within [0, 1]", ref)
		}
		if e.Probability != nil && e.When != "" {
			add("%s: set either probability or when, not both", ref)
		}
		if e.LatencyMs != nil && *e.LatencyMs < 0 {
			add("%s: latencyMs must be >= 0", ref)
		}
		if e.PayloadReqKB != nil && *e.PayloadReqKB < 0 || e.PayloadRespKB != nil && *e.PayloadRespKB < 0 {
			add("%s: payload sizes must be >= 0", ref)
		}
		if e.PoolSize != nil && *e.PoolSize < 0 {
			add("%s: poolSize must be >= 0", ref)
		}
		if e.TimeoutMs < 0 {
			add("%s: timeoutMs must be >= 0", ref)
		}
		for _, c := range e.Classes {
			if !classes[c] {
				add("%s: class %q is not defined in workload.classes", ref, c)
			}
		}
	}

	w := a.Workload
	if !ids[w.Entry] {
		add("workload.entry %q does not exist", w.Entry)
	}
	if !(w.TargetTPS > 0) {
		add("workload.targetTps must be > 0")
	}
	if w.Samples < 0 {
		add("workload.samples must be >= 0")
	}
	if t := w.Limits(); !(t.Warn > 0 && t.Warn < t.Overload && t.Overload <= 1) {
		add("workload.thresholds must satisfy 0 < warn < overload <= 1")
	}
	if tl := w.Timeline; tl != nil {
		if tl.DurationS < 0 || tl.DurationS > MaxDurationS {
			add("workload.timeline.durationS must be within [0, %d]", MaxDurationS)
		}
		if tl.QueueTimeoutMs < 0 {
			add("workload.timeline.queueTimeoutMs must be >= 0")
		}
		if tl.SamplesPerTick < 0 || tl.SamplesPerTick > MaxSamplesPerTick {
			add("workload.timeline.samplesPerTick must be within [0, %d]", MaxSamplesPerTick)
		}
		if p := tl.Load; p != nil {
			switch p.Kind {
			case LoadConstant:
			case LoadSpike:
				if !(p.PeakTPS > 0) {
					add("workload.timeline.load.peakTps must be > 0")
				}
				if p.StartS < 0 || p.RampS < 0 || p.HoldS < 0 {
					add("workload.timeline.load startS, rampS and holdS must be >= 0")
				}
			default:
				add("workload.timeline.load.kind must be %s or %s, got %q", LoadConstant, LoadSpike, p.Kind)
			}
		}
	}
	if w.SLO != nil {
		if w.SLO.P99Ms < 0 {
			add("workload.slo.p99Ms must be >= 0")
		}
		if r := w.SLO.ErrorRate; r != nil && (*r < 0 || *r > 1) {
			add("workload.slo.errorRate must be within [0, 1]")
		}
	}
	return errors.Join(errs...)
}
