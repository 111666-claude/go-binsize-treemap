package main

import (
	"testing"

	"github.com/nikolaydubina/treemap"
)

// Golden test for the original single-version conversion pipeline:
// it must stay unchanged by the diff mode additions.
func TestBasicSymtabConverterGolden(t *testing.T) {
	sf := parseLines(t, []string{
		" 100 T github.com/foo/a.KeepSame",
		" 200 T github.com/foo/a.Grow",
		"  50 T github.com/foo/a.Dup",
		"  70 T github.com/foo/a.Dup",
		"  10 U github.com/foo/a.Undef",
		"  30 T plaincppsymbol",
	})

	converter := BasicSymtabConverter{}
	tree := converter.SymtabFileToTreemap(sf)

	sizeImputer := treemap.SumSizeImputer{EmptyLeafSize: 0}
	sizeImputer.ImputeSize(tree)

	expectedSizes := map[string]float64{
		"github.com/foo/a/KeepSame": 100,
		"github.com/foo/a/Grow":     200,
		"github.com/foo/a/Dup":      120, // duplicate names are summed
		"github.com/foo/a":          420, // interior nodes sum children
		"github.com/foo":            420,
		"github.com":                420,
		"unknown/plaincppsymbol":    30, // package-less symbols go under unknown
		"unknown":                   30,
		rootNodeName:                450, // two roots => secret root sums everything
	}
	for path, want := range expectedSizes {
		node, ok := tree.Nodes[path]
		if !ok {
			t.Errorf("node %q is missing", path)
			continue
		}
		if !approxEqual(node.Size, want) {
			t.Errorf("node %q size: got %f, want %f", path, node.Size, want)
		}
		if node.HasHeat {
			t.Errorf("node %q must not have heat in single-version mode", path)
		}
	}

	if _, ok := tree.Nodes["github.com/foo/a/Undef"]; ok {
		t.Error("undefined symbols must be skipped")
	}

	if tree.Root != rootNodeName {
		t.Errorf("root: got %q, want secret root %q", tree.Root, rootNodeName)
	}
}
