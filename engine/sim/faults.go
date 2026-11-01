package sim

import (
	"fmt"
	"math"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// Failure injection (workload.timeline.failures). Outage, latency and
// failover faults are windows during which the Monte Carlo pass and the flow
// see the target as failed or slow. Instance loss is a one-off event:
// controllers (ReplicaSet, ASG, node group) replace what they manage after
// their usual startup or provisioning delay; unmanaged instances return when
// the failure ends.

// FailureSpan is an injected failure as shown on the timeline. EndS is nil
// when the failure lasts until the end; equal to StartS for a one-off event.
type FailureSpan struct {
	Kind   string   `json:"kind"`
	Target string   `json:"target,omitempty"`
	StartS float64  `json:"startS"`
	EndS   *float64 `json:"endS"`
	Label  string   `json:"label"`
}

type fault struct {
	f          model.Failure
	ref        string
	label      string
	start, end float64 // end is +Inf for "until the end"
	node       *gnode
	edge       *gedge
	pool       *poolState
	host       *hostState
	lost       map[*hostState]int // unmanaged instances to bring back at end
	began      bool
	ended      bool
}

// managed reports whether a controller replaces lost replicas: a Deployment's
// ReplicaSet or an autoscaling group.
func (hs *hostState) managed() bool {
	h := hs.h
	return h.host != nil && (h.host.arch == catalog.Orchestrator || h.auto != nil)
}

func (s *simState) resolveFaults() {
	g := s.g
	for i, f := range s.w.Timeline.Failures {
		ref := fmt.Sprintf("workload.timeline.failures[%d]", i)
		ft := &fault{f: f, ref: f.Target, start: f.AtS, end: math.Inf(1)}
		if f.DurationS > 0 {
			ft.end = f.AtS + f.DurationS
		}
		bad := func(format string, args ...any) { g.fail(fmt.Errorf(ref+": "+format, args...)) }
		n := g.byID[f.Target]
		callableTarget := n != nil && callable(n.arch) && n.arch != catalog.Client

		switch f.Kind {
		case model.FailInstanceLoss:
			unit := "instance(s)"
			switch {
			case n == nil:
				bad("target %q is not a node", f.Target)
			case n.arch == catalog.NodePool:
				ft.pool, unit = s.poolOf(n), "node(s)"
			default:
				ft.host = s.hostOf(n)
				if ft.host == nil {
					bad("instance-loss needs a node pool, Deployment, VM group or service, not %s %q", n.arch, f.Target)
				} else if ft.host.h.host != nil && ft.host.h.host.arch == catalog.Orchestrator {
					unit = "pod(s)"
				}
			}
			ft.label = fmt.Sprintf("%d %s lost", f.Instances(), unit)
		case model.FailZoneOutage:
			ft.label = fmt.Sprintf("zone outage (1 of %d zones)", f.ZoneCount())
		case model.FailOutage:
			if !callableTarget {
				bad("outage needs a component that receives calls, got %q", f.Target)
			}
			ft.node = n
			ft.label = "outage (" + f.OutageMode() + ")"
		case model.FailLatency:
			if callableTarget {
				ft.node = n
			} else {
				for _, e := range g.edges {
					if e.id == f.Target {
						ft.edge = e
					}
				}
				if ft.edge == nil {
					bad("latency needs a component that receives calls or an edge id, got %q", f.Target)
				}
			}
			ft.label = fmt.Sprintf("+%s latency", fmtMs(f.ExtraMs))
		case model.FailFailover:
			if n == nil || n.arch != catalog.Datastore {
				bad("failover needs a datastore, got %q", f.Target)
				break
			}
			ft.node = n
			if f.DurationS == 0 {
				ft.end = f.AtS + g.nonNeg(n, "failoverS")
			}
			ft.label = "primary failover"
		}

		span := FailureSpan{Kind: f.Kind, Target: f.Target, StartS: ft.start, Label: ft.label}
		switch {
		case f.Kind == model.FailInstanceLoss || f.Kind == model.FailZoneOutage:
			span.EndS = ptr(ft.start)
			if f.DurationS > 0 {
				span.EndS = ptr(ft.end)
			}
		case !math.IsInf(ft.end, 1):
			span.EndS = ptr(ft.end)
		}
		s.faults = append(s.faults, ft)
		s.out.Failures = append(s.out.Failures, span)
	}
}

func (s *simState) poolOf(n *gnode) *poolState {
	for _, ps := range s.pools {
		if ps.p.n == n {
			return ps
		}
	}
	return nil
}

// hostOf finds the replicas behind a Deployment, VM group or service.
func (s *simState) hostOf(n *gnode) *hostState {
	for _, hs := range s.hosts {
		if hs.h.svc == n || hs.h.host == n {
			return hs
		}
	}
	return nil
}

func fmtMs(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.3gs", ms/1000)
	}
	return fmt.Sprintf("%.3gms", ms)
}

// applyFaults sets this tick's fault state and fires one-off failures.
func (s *simState) applyFaults(t float64) {
	lv := s.lv
	clear(lv.down)
	clear(lv.primaryDown)
	clear(lv.extraMs)
	clear(lv.extraNetMs)
	for _, ft := range s.faults {
		active := t >= ft.start && t < ft.end
		if active && !ft.began {
			ft.began = true
			s.beginFault(ft, t)
		}
		if ft.began && !ft.ended && t >= ft.end {
			ft.ended = true
			s.endFault(ft, t)
		}
		if !active {
			continue
		}
		switch ft.f.Kind {
		case model.FailOutage:
			lv.down[ft.node.idx] = ft.f.OutageMode()
		case model.FailFailover:
			lv.primaryDown[ft.node.idx] = ft.f.OutageMode()
		case model.FailLatency:
			if ft.node != nil {
				lv.extraMs[ft.node.idx] += ft.f.ExtraMs
			} else {
				lv.extraNetMs[ft.edge.idx] += ft.f.ExtraMs
			}
		}
	}
}

func (s *simState) beginFault(ft *fault, t float64) {
	switch ft.f.Kind {
	case model.FailInstanceLoss:
		if ft.pool != nil {
			s.losePoolNodes(ft.pool, ft.f.Instances(), t)
		} else {
			s.loseReplicas(ft, ft.host, ft.f.Instances(), t)
		}
	case model.FailZoneOutage:
		zones := float64(ft.f.ZoneCount())
		s.event(t, "zone", "zone outage: 1 of %d zones fails (the zone holding the most instances)", ft.f.ZoneCount())
		for _, ps := range s.pools {
			s.losePoolNodes(ps, int(math.Ceil(float64(ps.ready)/zones)), t)
		}
		for _, hs := range s.hosts {
			if hs.h.pool == nil {
				s.loseReplicas(ft, hs, int(math.Ceil(float64(hs.ready)/zones)), t)
			}
		}
	default:
		s.event(t, ft.ref, "failure injected: %s", ft.label)
	}
}

func (s *simState) endFault(ft *fault, t float64) {
	switch ft.f.Kind {
	case model.FailInstanceLoss, model.FailZoneOutage:
		for _, hs := range s.hosts { // fixed order keeps events deterministic
			if k := ft.lost[hs]; k > 0 {
				hs.down -= k
				s.event(t, hs.ref, "%d lost instance(s) back", k)
			}
		}
	default:
		s.event(t, ft.ref, "failure ends: %s", ft.label)
	}
}

// losePoolNodes kills k ready nodes. Pods are assumed spread evenly, so each
// Deployment loses its share; the node group then restores its size.
func (s *simState) losePoolNodes(ps *poolState, k int, t float64) {
	k = min(k, ps.ready)
	if k == 0 {
		return
	}
	nodes := ps.ready
	ps.ready -= k
	for _, hs := range s.hosts {
		if hs.h.pool == ps.p {
			hs.ready -= int(math.Round(float64(hs.ready) * float64(k) / float64(nodes)))
		}
	}
	for range k {
		ps.provisioning = append(ps.provisioning, t+ps.provisionS)
	}
	s.event(t, ps.p.n.m.ID, "%d node(s) lost; node group replaces them in %.0fs", k, ps.provisionS)
}

func (s *simState) loseReplicas(ft *fault, hs *hostState, k int, t float64) {
	k = min(k, hs.ready)
	if k == 0 {
		return
	}
	hs.ready -= k
	if hs.managed() {
		s.event(t, hs.ref, "%d replica(s) lost; replaced after startup (%.0fs)", k, hs.startupS)
		return
	}
	hs.down += k
	if ft.lost == nil {
		ft.lost = map[*hostState]int{}
	}
	ft.lost[hs] += k
	if math.IsInf(ft.end, 1) {
		s.event(t, hs.ref, "%d instance(s) lost; nothing replaces them", k)
	} else {
		s.event(t, hs.ref, "%d instance(s) lost until t=%.0fs", k, ft.end)
	}
}
