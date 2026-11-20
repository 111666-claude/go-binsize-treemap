package main

import (
	"sort"
	"strings"

	"github.com/nikolaydubina/go-binsize-treemap/symtab"
	"github.com/nikolaydubina/treemap"
)

const (
	// deletedSymbolSizeFraction is the fraction of the total target version
	// size assigned to each deleted symbol, so that deletions stay visible
	// in a treemap whose areas otherwise encode target version sizes.
	deletedSymbolSizeFraction = 0.001

	// minDeletedSymbolSize is the lower bound in bytes for the area of a
	// deleted symbol (used when the target version is empty or tiny).
	minDeletedSymbolSize = 1.0

	// maxRelativeSizeChange bounds the relative size change encoded in color.
	// Changes beyond it saturate the color, so any input size ratio,
	// however extreme, maps into a bounded heat range.
	maxRelativeSizeChange = 1.0
)

// DiffStats summarizes differences between two symbol table versions.
type DiffStats struct {
	AddedSymbols     int // present only in target (with non-zero size)
	DeletedSymbols   int // present only in base (had non-zero size)
	ChangedSymbols   int // present in both, size changed
	UnchangedSymbols int // present in both, size identical
	AddedBytes       uint64
	DeletedBytes     uint64
	BaseBytes        uint64
	TargetBytes      uint64
	DeltaBytes       int64
}

// DiffSymtabConverter converts two parsed symtab files (base and target)
// into a single treemap.
//
// Size is the target version size in bytes. Deleted symbols get a small
// fixed size (deletedSymbolSizeFraction of the total target size) so that
// they remain visible. Heat encodes the relative size change in [0, 1]:
// 0 means maximal growth, 0.5 means unchanged, 1 means maximal shrinkage.
//
// Symbols are matched by full symbol name (which includes the package).
// Duplicate names within one version are summed. A symbol that moved to
// another package is deterministically reported as one deletion plus one
// addition. Symbols with zero size in both versions are ignored.
//
// The output is deterministic and does not depend on the input order of
// entries: symbols are processed in sorted order and children of every
// node are sorted, so the layout is stable across runs.
type DiffSymtabConverter struct {
	MaxDepth uint // number of levels from root, including, if 0 then no limit
}

func (s DiffSymtabConverter) SymtabDiffToTreemap(base, target symtab.SymtabFile) (treemap.Tree, DiffStats) {
	oldSizes := aggregateSymbolSizes(base.Entries)
	newSizes := aggregateSymbolSizes(target.Entries)

	var stats DiffStats
	for _, v := range oldSizes {
		stats.BaseBytes += v
	}
	for _, v := range newSizes {
		stats.TargetBytes += v
	}
	stats.DeltaBytes = int64(stats.TargetBytes) - int64(stats.BaseBytes)

	deletedSymbolSize := float64(stats.TargetBytes) * deletedSymbolSizeFraction
	if deletedSymbolSize < minDeletedSymbolSize {
		deletedSymbolSize = minDeletedSymbolSize
	}

	// union of symbol names in deterministic (sorted) order
	names := make([]string, 0, len(oldSizes)+len(newSizes))
	seen := map[string]bool{}
	for name := range oldSizes {
		seen[name] = true
		names = append(names, name)
	}
	for name := range newSizes {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	// per node aggregated sizes; interior nodes aggregate their subtree
	type nodeSizes struct {
		old     float64
		new     float64
		display float64
	}
	aggs := map[string]*nodeSizes{}
	getAgg := func(path string) *nodeSizes {
		a, ok := aggs[path]
		if !ok {
			a = &nodeSizes{}
			aggs[path] = a
		}
		return a
	}

	children := map[string]map[string]bool{}
	hasParent := map[string]bool{}

	for _, name := range names {
		oldSize := oldSizes[name]
		newSize := newSizes[name]

		switch {
		case oldSize == 0 && newSize == 0:
			continue // zero size in both versions: no area, no change
		case oldSize == 0:
			stats.AddedSymbols++
			stats.AddedBytes += newSize
		case newSize == 0:
			stats.DeletedSymbols++
			stats.DeletedBytes += oldSize
		case oldSize != newSize:
			stats.ChangedSymbols++
		default:
			stats.UnchangedSymbols++
		}

		display := float64(newSize)
		if newSize == 0 {
			display = deletedSymbolSize
		}

		parts := symbolPathParts(name)
		if s.MaxDepth > 0 && len(parts) > int(s.MaxDepth) {
			parts = parts[:s.MaxDepth]
		}

		for i := 1; i <= len(parts); i++ {
			path := strings.Join(parts[:i], "/")
			a := getAgg(path)
			a.old += float64(oldSize)
			a.new += float64(newSize)
			a.display += display

			if i == 1 {
				if _, ok := hasParent[path]; !ok {
					hasParent[path] = false
				}
				continue
			}
			parent := strings.Join(parts[:i-1], "/")
			if children[parent] == nil {
				children[parent] = map[string]bool{}
			}
			children[parent][path] = true
			hasParent[path] = true
		}
	}

	tree := treemap.Tree{
		Nodes: map[string]treemap.Node{},
		To:    map[string][]string{},
	}

	for path, a := range aggs {
		tree.Nodes[path] = treemap.Node{
			Path:    path,
			Size:    a.display,
			Heat:    heatForChange(a.old, a.new),
			HasHeat: true,
		}
	}
	for parent, set := range children {
		list := make([]string, 0, len(set))
		for child := range set {
			list = append(list, child)
		}
		sort.Strings(list)
		tree.To[parent] = list
	}

	var roots []string
	for node, has := range hasParent {
		if !has {
			roots = append(roots, node)
		}
	}
	sort.Strings(roots)

	switch {
	case len(roots) == 0:
		// empty diff: both versions had no usable symbols
	case len(roots) > 1:
		tree.Root = rootNodeName
		tree.To[tree.Root] = roots

		var oldSum, newSum, displaySum float64
		for _, root := range roots {
			a := aggs[root]
			oldSum += a.old
			newSum += a.new
			displaySum += a.display
		}
		tree.Nodes[tree.Root] = treemap.Node{
			Path:    rootNodeName,
			Size:    displaySum,
			Heat:    heatForChange(oldSum, newSum),
			HasHeat: true,
		}
	default:
		tree.Root = roots[0]
	}

	return tree, stats
}

// aggregateSymbolSizes sums symbol sizes by full symbol name.
// Undefined symbols are skipped, same as in the single-version converter.
func aggregateSymbolSizes(entries []symtab.SymtabEntry) map[string]uint64 {
	sizes := map[string]uint64{}
	for _, e := range entries {
		if e.Type == symtab.Undefined {
			continue
		}
		sizes[e.SymbolName] += uint64(e.Size)
	}
	return sizes
}

// heatForChange maps a size change into heat in [0, 1]:
// 0 is maximal growth, 0.5 is no change, 1 is maximal shrinkage.
// The relative change is clamped to [-maxRelativeSizeChange, +maxRelativeSizeChange],
// so added, deleted and extremely resized symbols all saturate at the bounds.
func heatForChange(oldSize, newSize float64) float64 {
	if oldSize <= 0 && newSize <= 0 {
		return 0.5
	}

	var relativeChange float64
	switch {
	case oldSize <= 0:
		relativeChange = maxRelativeSizeChange // added: maximal growth
	case newSize <= 0:
		relativeChange = -maxRelativeSizeChange // deleted: maximal shrinkage
	default:
		relativeChange = (newSize - oldSize) / oldSize
	}

	if relativeChange > maxRelativeSizeChange {
		relativeChange = maxRelativeSizeChange
	}
	if relativeChange < -maxRelativeSizeChange {
		relativeChange = -maxRelativeSizeChange
	}

	return 0.5 - 0.5*relativeChange/maxRelativeSizeChange
}
