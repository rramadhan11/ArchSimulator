package catalog

import "testing"

func TestEmbeddedCatalogIsValid(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"service.go", "gateway.kong", "k8s.deployment", "db.postgres", "cache.redis", "mq.kafka"} {
		if _, ok := c.Components[id]; !ok {
			t.Errorf("component %q missing", id)
		}
	}
}

func TestOptions(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	kong := c.Components["gateway.kong"]
	mults := kong.Params["pluginMultipliers"].Default.(map[string]any)
	for _, p := range c.AllowedValues(kong.Params["plugins"]) {
		if _, ok := mults[p]; !ok {
			t.Errorf("plugin option %q has no pluginMultipliers entry", p)
		}
	}
	if got := c.AllowedValues(c.Components["service.go"].Params["profile"]); len(got) != len(c.Profiles) {
		t.Errorf("profile options = %v, want every profile", got)
	}

	bad := Param{Default: "nope", Options: []string{"a", "b"}, Source: Source{Kind: SourceDefaultEstimate}}
	if c.checkOptions(bad) == nil {
		t.Error("default outside options must be rejected")
	}
	bad = Param{Default: "a", OptionsFrom: "regions", Source: Source{Kind: SourceDefaultEstimate}}
	if c.checkOptions(bad) == nil {
		t.Error("unknown optionsFrom must be rejected")
	}
}
