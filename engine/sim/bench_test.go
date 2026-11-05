package sim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// Native speed of the two engine calls on the bundled templates: the
// reference for the same calls in the browser (WASM), see README.md.
//
//	go test ./sim -run '^$' -bench . -benchtime 5x
func BenchmarkExamples(b *testing.B) {
	files, _ := filepath.Glob("../examples/*.json")
	cat, err := catalog.Load()
	if err != nil {
		b.Fatal(err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		a, err := model.Parse(data)
		if err != nil {
			b.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		b.Run(name+"/run", func(b *testing.B) {
			for range b.N {
				if _, err := Run(a, cat); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/simulate", func(b *testing.B) {
			for range b.N {
				if _, err := Simulate(a, cat); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
