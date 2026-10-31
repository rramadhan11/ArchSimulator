// Package catalog loads the component catalog: product-specific entries that
// map real configuration onto a small set of behavioural archetypes. The data
// is embedded so the engine stays IO-free (same binary for CLI, server, WASM).
package catalog

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed data
var dataFS embed.FS

type Archetype string

const (
	Client         Archetype = "client"
	EdgeProxy      Archetype = "edge_proxy"
	Gateway        Archetype = "gateway"
	ComputeService Archetype = "compute_service"
	RuntimeHost    Archetype = "runtime_host"
	Orchestrator   Archetype = "orchestrator"
	NodePool       Archetype = "node_pool"
	Datastore      Archetype = "datastore"
	Cache          Archetype = "cache"
	QueueStream    Archetype = "queue_stream"
	External       Archetype = "external"
)

var archetypes = map[Archetype]bool{
	Client: true, EdgeProxy: true, Gateway: true, ComputeService: true, RuntimeHost: true,
	Orchestrator: true, NodePool: true, Datastore: true, Cache: true, QueueStream: true, External: true,
}

// SourceKind says where a number comes from; it drives the confidence score.
type SourceKind string

const (
	SourceUser            SourceKind = "user"             // set by the user in the design
	SourceVendorDocs      SourceKind = "vendor-docs"      // documented product default or spec
	SourceVendorBenchmark SourceKind = "vendor-benchmark" // vendor benchmark, ideal conditions
	SourceDefaultEstimate SourceKind = "default-estimate" // our rough estimate
)

var sourceKinds = map[SourceKind]bool{
	SourceUser: true, SourceVendorDocs: true, SourceVendorBenchmark: true, SourceDefaultEstimate: true,
}

type Source struct {
	Kind SourceKind `yaml:"kind" json:"kind"`
	URL  string     `yaml:"url,omitempty" json:"url,omitempty"`
	Note string     `yaml:"note,omitempty" json:"note,omitempty"`
}

type Param struct {
	Default     any    `yaml:"default" json:"default"`
	Unit        string `yaml:"unit,omitempty" json:"unit,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Source      Source `yaml:"source" json:"source"`
	// Options lists the allowed values of a string (or string-list) param.
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	// OptionsFrom takes the allowed values from a catalog table instead:
	// instances | profiles | protocols.
	OptionsFrom string `yaml:"optionsFrom,omitempty" json:"optionsFrom,omitempty"`
}

type Component struct {
	ID          string           `yaml:"id" json:"id"`
	Archetype   Archetype        `yaml:"archetype" json:"archetype"`
	DisplayName string           `yaml:"displayName" json:"displayName"`
	Version     string           `yaml:"version,omitempty" json:"version,omitempty"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Params      map[string]Param `yaml:"params" json:"params"`
}

type Instance struct {
	VCPU      float64 `yaml:"vcpu" json:"vcpu"`
	MemoryGiB float64 `yaml:"memoryGiB" json:"memoryGiB"`
}

type NetworkClass struct {
	RTTMs         float64 `yaml:"rttMs" json:"rttMs"`
	BandwidthMbps float64 `yaml:"bandwidthMbps" json:"bandwidthMbps"`
	Source        Source  `yaml:"source" json:"source"`
}

type Protocol struct {
	SerializeMsPerKB float64 `yaml:"serializeMsPerKB" json:"serializeMsPerKB"`
	Multiplexed      bool    `yaml:"multiplexed" json:"multiplexed"`
	DefaultPoolSize  int     `yaml:"defaultPoolSize" json:"defaultPoolSize"`
	Source           Source  `yaml:"source" json:"source"`
}

// Profile is a preset workload shape for compute services.
type Profile struct {
	CPUMsPerReq float64 `yaml:"cpuMsPerReq" json:"cpuMsPerReq"`
	IOWaitMs    float64 `yaml:"ioWaitMs" json:"ioWaitMs"`
	Source      Source  `yaml:"source" json:"source"`
}

type Catalog struct {
	Version         string                  `json:"version"`
	Components      map[string]*Component   `json:"components"`
	Instances       map[string]Instance     `json:"instances"`
	InstancesSource Source                  `json:"instancesSource"`
	NetworkClasses  map[string]NetworkClass `json:"networkClasses"`
	Protocols       map[string]Protocol     `json:"protocols"`
	Profiles        map[string]Profile      `json:"profiles"`
}

type sharedFile struct {
	Version        string                  `yaml:"version"`
	Profiles       map[string]Profile      `yaml:"profiles"`
	NetworkClasses map[string]NetworkClass `yaml:"networkClasses"`
	Protocols      map[string]Protocol     `yaml:"protocols"`
}

type instancesFile struct {
	Source    Source              `yaml:"source"`
	Instances map[string]Instance `yaml:"instances"`
}

// Load returns the embedded catalog. It is parsed once and must be treated as
// read-only by callers.
var Load = sync.OnceValues(func() (*Catalog, error) { return load(dataFS) })

func load(fsys fs.FS) (*Catalog, error) {
	var shared sharedFile
	if err := decodeFile(fsys, "data/shared.yaml", &shared); err != nil {
		return nil, err
	}
	var inst instancesFile
	if err := decodeFile(fsys, "data/instances.yaml", &inst); err != nil {
		return nil, err
	}
	c := &Catalog{
		Version:         shared.Version,
		Components:      map[string]*Component{},
		Instances:       inst.Instances,
		InstancesSource: inst.Source,
		NetworkClasses:  shared.NetworkClasses,
		Protocols:       shared.Protocols,
		Profiles:        shared.Profiles,
	}

	files, err := fs.Glob(fsys, "data/components/*.yaml")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		var comp Component
		if err := decodeFile(fsys, f, &comp); err != nil {
			return nil, err
		}
		if _, dup := c.Components[comp.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate component id %q", f, comp.ID)
		}
		c.Components[comp.ID] = &comp
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func decodeFile(fsys fs.FS, name string, out any) error {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (c *Catalog) validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	checkSource := func(ref string, s Source) {
		if !sourceKinds[s.Kind] || s.Kind == SourceUser {
			add("%s: invalid source kind %q", ref, s.Kind)
		}
	}

	if c.Version == "" {
		add("shared.yaml: version is required")
	}
	checkSource("instances", c.InstancesSource)
	for name, in := range c.Instances {
		if !(in.VCPU > 0) || !(in.MemoryGiB > 0) {
			add("instance %q: vcpu and memoryGiB must be > 0", name)
		}
	}
	for name, n := range c.NetworkClasses {
		checkSource("networkClass "+name, n.Source)
		if n.RTTMs < 0 || !(n.BandwidthMbps > 0) {
			add("networkClass %q: rttMs must be >= 0 and bandwidthMbps > 0", name)
		}
	}
	for name, p := range c.Protocols {
		checkSource("protocol "+name, p.Source)
	}
	for name, p := range c.Profiles {
		checkSource("profile "+name, p.Source)
	}
	for id, comp := range c.Components {
		if !archetypes[comp.Archetype] {
			add("component %q: unknown archetype %q", id, comp.Archetype)
		}
		if comp.DisplayName == "" {
			add("component %q: displayName is required", id)
		}
		for key, p := range comp.Params {
			ref := "component " + id + " param " + key
			checkSource(ref, p.Source)
			if err := c.checkOptions(p); err != nil {
				add("%s: %v", ref, err)
			}
		}
	}
	return errors.Join(errs...)
}

// AllowedValues returns the values a param accepts, or nil when it is free-form.
func (c *Catalog) AllowedValues(p Param) []string {
	switch p.OptionsFrom {
	case "instances":
		return sortedKeys(c.Instances)
	case "profiles":
		return sortedKeys(c.Profiles)
	case "protocols":
		return sortedKeys(c.Protocols)
	}
	return p.Options
}

func (c *Catalog) checkOptions(p Param) error {
	switch p.OptionsFrom {
	case "", "instances", "profiles", "protocols":
	default:
		return fmt.Errorf("unknown optionsFrom %q", p.OptionsFrom)
	}
	if p.OptionsFrom != "" && len(p.Options) > 0 {
		return errors.New("set either options or optionsFrom, not both")
	}
	allowed := c.AllowedValues(p)
	if allowed == nil {
		return nil
	}
	var values []any
	switch d := p.Default.(type) {
	case string:
		values = []any{d}
	case []any:
		values = d
	default:
		return fmt.Errorf("options need a string or list default, got %T", p.Default)
	}
	for _, v := range values {
		if s, ok := v.(string); !ok || !slices.Contains(allowed, s) {
			return fmt.Errorf("default %v is not one of %s", v, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ComponentIDs returns component ids sorted for stable output.
func (c *Catalog) ComponentIDs() []string { return sortedKeys(c.Components) }

// ProtocolNames returns protocol names sorted for stable output.
func (c *Catalog) ProtocolNames() string { return strings.Join(sortedKeys(c.Protocols), ", ") }
