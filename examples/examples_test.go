package examples_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/lint"
)

func TestExamplesValidate(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("*/*.json")
	if err != nil {
		t.Fatal(err)
	}
	yamlPaths, err := filepath.Glob("*/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, yamlPaths...)
	if len(paths) == 0 {
		t.Fatal("no examples found")
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var pipeline *graph.Graph
			if filepath.Ext(path) == ".yaml" {
				pipeline, err = graph.ParseYAML(raw)
			} else {
				pipeline, err = graph.Parse(raw)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lint.ValidateOrError(*pipeline); err != nil {
				t.Fatal(err)
			}
		})
	}
}
