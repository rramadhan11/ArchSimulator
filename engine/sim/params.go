package sim

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"archsim/engine/catalog"
)

// Assumption records one parameter value the engine used and where it came from.
type Assumption struct {
	Node   string         `json:"node"`
	Param  string         `json:"param"`
	Value  any            `json:"value"`
	Source catalog.Source `json:"source"`
}

var userSource = catalog.Source{Kind: catalog.SourceUser}

// paramLog deduplicates assumptions by node/param and keeps first-use order.
type paramLog struct {
	byKey map[string]Assumption
	order []string
}

func newParamLog() *paramLog { return &paramLog{byKey: map[string]Assumption{}} }

func paramKey(node, param string) string { return node + "/" + param }

func (l *paramLog) record(node, param string, v any, src catalog.Source) string {
	k := paramKey(node, param)
	if _, ok := l.byKey[k]; !ok {
		l.order = append(l.order, k)
		l.byKey[k] = Assumption{Node: node, Param: param, Value: v, Source: src}
	}
	return k
}

func (l *paramLog) source(key string) catalog.SourceKind { return l.byKey[key].Source.Kind }

func (l *paramLog) list() []Assumption {
	out := make([]Assumption, 0, len(l.order))
	for _, k := range l.order {
		out = append(out, l.byKey[k])
	}
	return out
}

// configValue walks a dotted path ("hpa.max") through nested config maps.
func configValue(cfg map[string]any, path string) (any, bool) {
	var cur any = cfg
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// The readers below resolve node config first, then the catalog default, and
// record the value as an assumption. The first failure is kept in g.err (same
// pattern as bufio.Scanner) so build code stays linear; callers check g.err at
// stage boundaries.

func (g *graph) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

func (g *graph) raw(n *gnode, key string) (any, catalog.Source, bool) {
	if v, ok := configValue(n.m.Config, key); ok {
		return v, userSource, true
	}
	if p, ok := n.comp.Params[key]; ok {
		return p.Default, p.Source, true
	}
	return nil, catalog.Source{}, false
}

func (g *graph) has(n *gnode, key string) bool {
	_, ok := configValue(n.m.Config, key)
	return ok
}

func (g *graph) mustRaw(n *gnode, key string) (any, catalog.Source) {
	v, src, ok := g.raw(n, key)
	if !ok {
		g.fail(fmt.Errorf("node %q (%s): parameter %q is not set and has no catalog default", n.m.ID, n.m.Kind, key))
	}
	return v, src
}

func (g *graph) num(n *gnode, key string) float64 {
	v, src := g.mustRaw(n, key)
	f, err := toFloat(v)
	if err != nil {
		g.fail(fmt.Errorf("node %q: %s: %w", n.m.ID, key, err))
		return 0
	}
	g.params.record(n.m.ID, key, f, src)
	return f
}

// nonNeg reads a number that must be >= 0.
func (g *graph) nonNeg(n *gnode, key string) float64 {
	f := g.num(n, key)
	if f < 0 {
		g.fail(fmt.Errorf("node %q: %s must be >= 0, got %v", n.m.ID, key, f))
	}
	return f
}

// positive reads a number that must be > 0.
func (g *graph) positive(n *gnode, key string) float64 {
	f := g.num(n, key)
	if !(f > 0) {
		g.fail(fmt.Errorf("node %q: %s must be > 0, got %v", n.m.ID, key, f))
	}
	return f
}

// count reads a whole number >= min.
func (g *graph) count(n *gnode, key string, min int) int {
	f := g.num(n, key)
	if f != math.Trunc(f) || f < float64(min) {
		g.fail(fmt.Errorf("node %q: %s must be a whole number >= %d, got %v", n.m.ID, key, min, f))
		return min
	}
	return int(f)
}

func (g *graph) str(n *gnode, key string) string {
	v, src := g.mustRaw(n, key)
	s, ok := v.(string)
	if !ok {
		g.fail(fmt.Errorf("node %q: %s must be a string, got %T", n.m.ID, key, v))
	}
	g.params.record(n.m.ID, key, s, src)
	return s
}

func (g *graph) boolean(n *gnode, key string) bool {
	v, src := g.mustRaw(n, key)
	b, ok := v.(bool)
	if !ok {
		g.fail(fmt.Errorf("node %q: %s must be a boolean, got %T", n.m.ID, key, v))
	}
	g.params.record(n.m.ID, key, b, src)
	return b
}

func (g *graph) strList(n *gnode, key string) []string {
	v, src := g.mustRaw(n, key)
	items, ok := v.([]any)
	if !ok {
		g.fail(fmt.Errorf("node %q: %s must be a list, got %T", n.m.ID, key, v))
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			g.fail(fmt.Errorf("node %q: %s must contain strings, got %T", n.m.ID, key, it))
			return nil
		}
		out = append(out, s)
	}
	g.params.record(n.m.ID, key, out, src)
	return out
}

// numMap reads a map of non-negative numbers, merging user entries over the
// catalog default so a user can override one entry without restating all.
func (g *graph) numMap(n *gnode, key string) map[string]float64 {
	out := map[string]float64{}
	src := userSource
	if p, ok := n.comp.Params[key]; ok {
		src = p.Source
		if !g.mergeNumMap(n, key, p.Default, out) {
			return nil
		}
	}
	if v, ok := configValue(n.m.Config, key); ok {
		src = userSource
		if !g.mergeNumMap(n, key, v, out) {
			return nil
		}
	}
	g.params.record(n.m.ID, key, out, src)
	return out
}

func (g *graph) mergeNumMap(n *gnode, key string, v any, into map[string]float64) bool {
	m, ok := v.(map[string]any)
	if !ok {
		g.fail(fmt.Errorf("node %q: %s must be a map, got %T", n.m.ID, key, v))
		return false
	}
	for k, raw := range m {
		f, err := toFloat(raw)
		if err != nil || f < 0 {
			g.fail(fmt.Errorf("node %q: %s.%s must be a number >= 0", n.m.ID, key, k))
			return false
		}
		into[k] = f
	}
	return true
}

// cpuCores reads a Kubernetes CPU quantity ("500m", "2", 0.5). Empty string
// means "not set" and returns 0.
func (g *graph) cpuCores(n *gnode, key string) float64 {
	v, src := g.mustRaw(n, key)
	f, err := parseCPU(v)
	if err != nil {
		g.fail(fmt.Errorf("node %q: %s: %w", n.m.ID, key, err))
	}
	g.params.record(n.m.ID, key, v, src)
	return f
}

// memBytes reads a Kubernetes memory quantity ("512Mi", "1Gi", 536870912).
func (g *graph) memBytes(n *gnode, key string) float64 {
	v, src := g.mustRaw(n, key)
	f, err := parseMemory(v)
	if err != nil {
		g.fail(fmt.Errorf("node %q: %s: %w", n.m.ID, key, err))
	}
	g.params.record(n.m.ID, key, v, src)
	return f
}

func (g *graph) instance(n *gnode) catalog.Instance {
	name := g.str(n, "instanceType")
	in, ok := g.cat.Instances[name]
	if !ok {
		g.fail(fmt.Errorf("node %q: unknown instanceType %q (known: %s)", n.m.ID, name, strings.Join(sortedKeys(g.cat.Instances), ", ")))
	}
	return in
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case uint64:
		return float64(x), nil
	default:
		return 0, fmt.Errorf("expected a number, got %T (%v)", v, v)
	}
}

func parseCPU(v any) (float64, error) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, nil
		}
		scale := 1.0
		if strings.HasSuffix(s, "m") {
			scale, s = 0.001, strings.TrimSuffix(s, "m")
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("invalid CPU quantity %q", v)
		}
		return f * scale, nil
	}
	f, err := toFloat(v)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid CPU quantity %v", v)
	}
	return f, nil
}

var memSuffixes = []struct {
	suffix string
	scale  float64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
	{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
}

func parseMemory(v any) (float64, error) {
	s, ok := v.(string)
	if !ok {
		f, err := toFloat(v)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("invalid memory quantity %v", v)
		}
		return f, nil
	}
	s = strings.TrimSpace(s)
	scale := 1.0
	for _, m := range memSuffixes {
		if strings.HasSuffix(s, m.suffix) {
			scale, s = m.scale, strings.TrimSuffix(s, m.suffix)
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid memory quantity %q", v)
	}
	return f * scale, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
