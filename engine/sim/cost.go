package sim

import (
	"slices"
	"strings"

	"archsim/engine/catalog"
)

// Cost covers what is billed per instance hour: Kubernetes node pools, VM
// groups and managed databases. Rates come from the design's pricing, else
// the catalog; an instance without either is listed as unpriced, never
// counted as free. Load balancers, queues, caches, external APIs, storage
// and traffic are not included.

const hoursPerMonth = 730

// Cost is the steady-state bill at the evaluated load.
type Cost struct {
	MonthlyUSD float64    `json:"monthlyUSD"` // priced items only
	Items      []CostItem `json:"items"`
	Unpriced   []string   `json:"unpriced,omitempty"` // rate keys without a price
}

// CostItem is one billed component: Count instances at a rate.
type CostItem struct {
	Node       string   `json:"node"`
	Rate       string   `json:"rate"` // pricing key: instance type, "<type>/<engine>" for databases
	Count      int      `json:"count"`
	HourlyUSD  *float64 `json:"hourlyUSD"` // per instance; nil when unpriced
	MonthlyUSD *float64 `json:"monthlyUSD"`
	Source     string   `json:"source,omitempty"` // "yours" or "catalog"
}

// billable is a component billed per instance, resolved at compile time.
type billable struct {
	node  *gnode
	rate  string
	usd   float64 // per instance hour
	src   string  // "" when unpriced
	pool  *nodePool
	host  *hosting
	fixed int // datastores: primary + read replicas
}

// rate looks up the design's own rate, then the catalog's.
func (g *graph) rate(key string) (float64, string) {
	if p := g.a.Pricing; p != nil {
		if usd, ok := p.HourlyUSD[key]; ok {
			return usd, "yours"
		}
	}
	if usd, ok := g.cat.HourlyUSD[key]; ok {
		return usd, "catalog"
	}
	return 0, ""
}

func (g *graph) addBillable(b billable) {
	b.usd, b.src = g.rate(b.rate)
	g.billables = append(g.billables, b)
}

// collectBillables runs after the graph is built, once pools and hosts exist.
func (g *graph) collectBillables() {
	for _, p := range g.pools {
		g.addBillable(billable{node: p.n, rate: g.str(p.n, "instanceType"), pool: p})
	}
	for _, h := range g.hosts {
		if h.host != nil && h.host.arch == catalog.RuntimeHost {
			g.addBillable(billable{node: h.host, rate: g.str(h.host, "instanceType"), host: h})
		}
	}
	for _, n := range g.nodes {
		if _, sized := n.comp.Params["instanceType"]; n.arch == catalog.Datastore && sized {
			engine := strings.TrimPrefix(n.m.Kind, "db.")
			g.addBillable(billable{node: n, rate: g.str(n, "instanceType") + "/" + engine, fixed: 1 + g.count(n, "readReplicas", 0)})
		}
	}
}

// instances is how many instances b runs in the evaluated steady state.
func (b *billable) instances() int {
	switch {
	case b.pool != nil:
		if b.pool.autoscaled {
			return max(b.pool.minNodes, b.pool.nodesNeeded)
		}
		return b.pool.minNodes
	case b.host != nil:
		return b.host.scheduled
	}
	return b.fixed
}

// cost prices the evaluated steady state.
func (g *graph) cost() *Cost {
	if len(g.billables) == 0 {
		return nil
	}
	c := &Cost{}
	for i := range g.billables {
		b := &g.billables[i]
		it := CostItem{Node: b.node.m.ID, Rate: b.rate, Count: b.instances(), Source: b.src}
		if b.src != "" {
			monthly := b.usd * float64(it.Count) * hoursPerMonth
			it.HourlyUSD, it.MonthlyUSD = ptr(b.usd), ptr(monthly)
			c.MonthlyUSD += monthly
		} else if !slices.Contains(c.Unpriced, b.rate) {
			c.Unpriced = append(c.Unpriced, b.rate)
		}
		c.Items = append(c.Items, it)
	}
	return c
}
