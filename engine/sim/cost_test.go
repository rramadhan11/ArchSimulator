package sim

import (
	"os"
	"strings"
	"testing"

	"archsim/engine/model"
)

// costDoc: an API on a fixed group of two c7i.large VMs and one Postgres.
func costDoc(vm, pricing string) string {
	return `{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "vms", "kind": "vm.ec2", "config": {"instanceType": "` + vm + `", "count": 2}},
	    {"id": "api", "kind": "service.go", "parent": "vms"},
	    {"id": "pg", "kind": "db.postgres", "config": {"instanceType": "db.r6g.large"}}
	  ],
	  "edges": [{"from": "c", "to": "api"}, {"from": "api", "to": "pg", "op": "read"}],
	  "workload": {"entry": "c", "targetTps": 100}` + pricing + `
	}`
}

// Jakarta prices from the catalog: c7i.large $0.1029/h, db.r6g.large
// Postgres $0.27/h; a month is 730 h.
func TestCostFromCatalog(t *testing.T) {
	c := mustRun(t, costDoc("c7i.large", "")).Target.Cost
	if c == nil || len(c.Items) != 2 || len(c.Unpriced) != 0 {
		t.Fatalf("cost = %+v", c)
	}
	within(t, "VMs", *c.Items[0].MonthlyUSD, 2*0.1029*730, 1e-9)
	within(t, "Postgres", *c.Items[1].MonthlyUSD, 0.27*730, 1e-9)
	within(t, "total", c.MonthlyUSD, (2*0.1029+0.27)*730, 1e-9)
	if c.Items[1].Rate != "db.r6g.large/postgres" || c.Items[0].Source != "catalog" {
		t.Errorf("items = %+v", c.Items)
	}
}

// The design's own rate wins; a type without any rate is listed, not free.
func TestCostOverridesAndUnpriced(t *testing.T) {
	c := mustRun(t, costDoc("c7i.large", `, "pricing": {"hourlyUSD": {"c7i.large": 0.2}}`)).Target.Cost
	if it := c.Items[0]; it.Source != "yours" || *it.HourlyUSD != 0.2 {
		t.Errorf("override: %+v", it)
	}
	c = mustRun(t, costDoc("generic-2c4g", "")).Target.Cost
	if it := c.Items[0]; it.HourlyUSD != nil || it.MonthlyUSD != nil || strings.Join(c.Unpriced, ",") != "generic-2c4g" {
		t.Errorf("unpriced: %+v, %v", it, c.Unpriced)
	}
	within(t, "total without the unpriced VMs", c.MonthlyUSD, 0.27*730, 1e-9)
}

// Over a 600 s window the fixed VMs and the database cost a sixth of an hour.
func TestTimelineCost(t *testing.T) {
	out := mustSimulate(t, costDoc("c7i.large", ""), model.Timeline{DurationS: 600, SamplesPerTick: 200})
	if out.Summary.CostUSD == nil {
		t.Fatal("no cost")
	}
	within(t, "window cost", *out.Summary.CostUSD, (2*0.1029+0.27)*600/3600, 1e-9)
}

// Every catalog default and template prices at least something, so the
// cost view is not empty out of the box.
func TestTemplatesArePriced(t *testing.T) {
	for _, f := range []string{"rest-postgres", "k8s-kong-microservices", "event-driven-kafka", "k8s-spike", "retry-storm"} {
		data, err := os.ReadFile("../examples/" + f + ".json")
		if err != nil {
			t.Fatal(err)
		}
		res := mustRun(t, string(data))
		if c := res.Target.Cost; c == nil || len(c.Unpriced) > 0 || c.MonthlyUSD <= 0 {
			t.Errorf("%s: cost %+v", f, c)
		}
	}
}
