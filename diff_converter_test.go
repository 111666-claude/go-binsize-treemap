package main

import (
	"image/color"
	"math"
	"reflect"
	"testing"

	"github.com/nikolaydubina/go-binsize-treemap/symtab"
	"github.com/nikolaydubina/treemap"
	"github.com/nikolaydubina/treemap/render"
)

// fixed two-version symbol lists used by all diff tests
func diffFixtureBaseLines() []string {
	return []string{
		" 100 T github.com/foo/a.KeepSame",
		" 200 T github.com/foo/a.Grow",
		" 300 T github.com/foo/a.Shrink",
		" 400 T github.com/foo/a.DeleteMe",
		" 500 T github.com/foo/b.Move",
		"  50 T github.com/foo/a.Dup",
		"  70 T github.com/foo/a.Dup",
		"   0 T github.com/foo/a.Zero",
		"  10 U github.com/foo/a.Undef",
	}
}

func diffFixtureTargetLines() []string {
	return []string{
		" 100 T github.com/foo/a.KeepSame",
		" 400 T github.com/foo/a.Grow",
		" 150 T github.com/foo/a.Shrink",
		" 600 T github.com/foo/c.Move",
		" 800 T github.com/foo/a.BrandNew",
		" 120 T github.com/foo/a.Dup",
		"   0 T github.com/foo/a.Zero",
	}
}

func parseLines(t *testing.T, lines []string) symtab.SymtabFile {
	t.Helper()
	parser := symtab.GoSymtabParser{}
	f, err := parser.ParseSymtab(lines)
	if err != nil || f == nil {
		t.Fatalf("can not parse symtab: %v", err)
	}
	return *f
}

func diffFixtureTree(t *testing.T) (treemap.Tree, DiffStats) {
	t.Helper()
	base := parseLines(t, diffFixtureBaseLines())
	target := parseLines(t, diffFixtureTargetLines())
	converter := DiffSymtabConverter{}
	tree, stats := converter.SymtabDiffToTreemap(base, target)
	return tree, stats
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestDiffStats(t *testing.T) {
	_, stats := diffFixtureTree(t)

	expected := DiffStats{
		AddedSymbols:     2,    // BrandNew, c.Move (migrated here)
		DeletedSymbols:   2,    // DeleteMe, b.Move (migrated away)
		ChangedSymbols:   2,    // Grow, Shrink
		UnchangedSymbols: 2,    // KeepSame, Dup (50+70 == 120)
		AddedBytes:       1400, // BrandNew(800) + c.Move(600)
		DeletedBytes:     900,  // DeleteMe(400) + b.Move(500)
		BaseBytes:        1620,
		TargetBytes:      2170,
		DeltaBytes:       550,
	}
	if stats != expected {
		t.Fatalf("wrong stats:\ngot:  %+v\nwant: %+v", stats, expected)
	}
}

func TestDiffTreeSizes(t *testing.T) {
	tree, _ := diffFixtureTree(t)

	deletedSize := 2170.0 * deletedSymbolSizeFraction // 2.17

	expectedSizes := map[string]float64{
		"github.com/foo/a/KeepSame": 100,
		"github.com/foo/a/Grow":     400,
		"github.com/foo/a/Shrink":   150,
		"github.com/foo/a/DeleteMe": deletedSize, // deleted: small fixed size to stay visible
		"github.com/foo/a/BrandNew": 800,
		"github.com/foo/a/Dup":      120,
		"github.com/foo/b/Move":     deletedSize, // migrated away: deletion
		"github.com/foo/c/Move":     600,         // migrated here: addition
		// interior nodes aggregate display sizes of their subtree
		"github.com/foo/a": 100 + 400 + 150 + deletedSize + 800 + 120,
		"github.com/foo/b": deletedSize,
		"github.com/foo/c": 600,
		"github.com/foo":   2170 + 2*deletedSize,
		"github.com":       2170 + 2*deletedSize,
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
		if !node.HasHeat {
			t.Errorf("node %q has no heat", path)
		}
	}

	// zero-size-in-both and undefined symbols must not appear
	for _, absent := range []string{
		"github.com/foo/a/Zero",
		"github.com/foo/a/Undef",
	} {
		if _, ok := tree.Nodes[absent]; ok {
			t.Errorf("node %q must not be present", absent)
		}
	}

	// single top-level package => root is that package
	if tree.Root != "github.com" {
		t.Errorf("root: got %q, want %q", tree.Root, "github.com")
	}
}

func TestDiffHeat(t *testing.T) {
	tree, _ := diffFixtureTree(t)

	expectedHeat := map[string]float64{
		"github.com/foo/a/KeepSame": 0.5,  // unchanged
		"github.com/foo/a/Grow":     0.0,  // doubled: saturates at max growth
		"github.com/foo/a/Shrink":   0.75, // halved
		"github.com/foo/a/DeleteMe": 1.0,  // deleted: max shrinkage
		"github.com/foo/a/BrandNew": 0.0,  // added: max growth
		"github.com/foo/a/Dup":      0.5,  // 50+70 == 120: unchanged
		"github.com/foo/b/Move":     1.0,  // migrated away
		"github.com/foo/c/Move":     0.0,  // migrated here
		// package aggregates: heat from summed old/new sizes
		"github.com/foo/a": 0.5 - 0.5*(1570.0-1120.0)/1120.0,
		"github.com/foo/b": 1.0,
		"github.com/foo/c": 0.0,
		"github.com/foo":   0.5 - 0.5*(2170.0-1620.0)/1620.0,
	}

	for path, want := range expectedHeat {
		node, ok := tree.Nodes[path]
		if !ok {
			t.Errorf("node %q is missing", path)
			continue
		}
		if !approxEqual(node.Heat, want) {
			t.Errorf("node %q heat: got %f, want %f", path, node.Heat, want)
		}
	}

	// heat is always bounded in [0, 1]
	for path, node := range tree.Nodes {
		if node.Heat < 0 || node.Heat > 1 {
			t.Errorf("node %q heat %f out of [0, 1]", path, node.Heat)
		}
	}
}

func TestDiffColors(t *testing.T) {
	tree, _ := diffFixtureTree(t)
	treemap.SetNamesFromPaths(&tree)

	palette, ok := render.GetPalette("RdBu")
	if !ok {
		t.Fatal("RdBu palette is not available")
	}
	colorer := render.HeatColorer{Palette: palette}

	toRGBA := func(path string) color.RGBA {
		return color.RGBAModel.Convert(colorer.ColorBox(tree, path)).(color.RGBA)
	}

	// grown symbols are red-ish
	for _, path := range []string{"github.com/foo/a/Grow", "github.com/foo/a/BrandNew"} {
		c := toRGBA(path)
		if c.R <= c.B {
			t.Errorf("node %q color %+v: expected red dominance (grew)", path, c)
		}
	}

	// shrunk and deleted symbols are blue-ish
	for _, path := range []string{"github.com/foo/a/Shrink", "github.com/foo/a/DeleteMe"} {
		c := toRGBA(path)
		if c.B <= c.R {
			t.Errorf("node %q color %+v: expected blue dominance (shrank)", path, c)
		}
	}

	// unchanged symbols are neutral (grey/white midpoint)
	c := toRGBA("github.com/foo/a/KeepSame")
	if d := int(c.R) - int(c.B); d < -10 || d > 10 {
		t.Errorf("unchanged node color %+v: expected neutral", c)
	}
}

func TestDiffRectAreas(t *testing.T) {
	tree, _ := diffFixtureTree(t)
	treemap.SetNamesFromPaths(&tree)

	// no per-box margin: rendered box areas must be proportional to sizes
	uiBuilder := render.UITreeMapBuilder{Colorer: render.NoneColorer{}}
	spec := uiBuilder.NewUITreeMap(tree, 1024, 1024, 0, 4, 16)

	findBox := func(title string) *render.UIBox {
		var found *render.UIBox
		var walk func(b *render.UIBox)
		walk = func(b *render.UIBox) {
			if b.Title != nil && b.Title.Text == title {
				found = b
			}
			for i := range b.Children {
				walk(&b.Children[i])
			}
		}
		walk(&spec)
		return found
	}

	grow := findBox("Grow")
	keep := findBox("KeepSame")
	if grow == nil || keep == nil {
		t.Fatalf("can not find boxes: Grow=%v KeepSame=%v", grow, keep)
	}

	// Grow is 400B, KeepSame is 100B: areas must be in ratio 4:1
	got := (grow.W * grow.H) / (keep.W * keep.H)
	if math.Abs(got-4.0) > 0.01 {
		t.Errorf("area ratio Grow/KeepSame: got %f, want 4.0", got)
	}
}

func TestDiffOrderIndependent(t *testing.T) {
	reversed := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[len(lines)-1-i] = l
		}
		return out
	}

	base1 := parseLines(t, diffFixtureBaseLines())
	target1 := parseLines(t, diffFixtureTargetLines())
	base2 := parseLines(t, reversed(diffFixtureBaseLines()))
	target2 := parseLines(t, reversed(diffFixtureTargetLines()))

	converter := DiffSymtabConverter{}
	tree1, stats1 := converter.SymtabDiffToTreemap(base1, target1)
	tree2, stats2 := converter.SymtabDiffToTreemap(base2, target2)

	if stats1 != stats2 {
		t.Errorf("stats differ under reordering:\n%+v\n%+v", stats1, stats2)
	}
	if !reflect.DeepEqual(tree1, tree2) {
		t.Errorf("trees differ under reordering:\n%+v\n%+v", tree1, tree2)
	}
}

func TestDiffDuplicateNames(t *testing.T) {
	// duplicates within one version are summed before matching
	base := parseLines(t, []string{
		" 50 T github.com/foo/a.F",
		" 70 T github.com/foo/a.F",
	})
	target := parseLines(t, []string{
		" 60 T github.com/foo/a.F",
		" 60 T github.com/foo/a.F",
	})

	converter := DiffSymtabConverter{}
	tree, stats := converter.SymtabDiffToTreemap(base, target)

	if stats.UnchangedSymbols != 1 || stats.ChangedSymbols != 0 {
		t.Errorf("expected single unchanged symbol, got %+v", stats)
	}
	node := tree.Nodes["github.com/foo/a/F"]
	if !approxEqual(node.Size, 120) || !approxEqual(node.Heat, 0.5) {
		t.Errorf("duplicate node: got size=%f heat=%f, want 120 and 0.5", node.Size, node.Heat)
	}
}

func TestDiffExtremeSizeDifference(t *testing.T) {
	converter := DiffSymtabConverter{}

	// 1 byte -> 10MB: unbounded ratio, but heat saturates at max growth
	tree, _ := converter.SymtabDiffToTreemap(
		parseLines(t, []string{" 1 T github.com/foo/a.X"}),
		parseLines(t, []string{" 10000000 T github.com/foo/a.X"}),
	)
	if h := tree.Nodes["github.com/foo/a/X"].Heat; !approxEqual(h, 0) {
		t.Errorf("1B -> 10MB heat: got %f, want 0 (saturated growth)", h)
	}

	// 10MB -> 1 byte: approaches max shrinkage (ratio asymptotes to -100%)
	tree, _ = converter.SymtabDiffToTreemap(
		parseLines(t, []string{" 10000000 T github.com/foo/a.X"}),
		parseLines(t, []string{" 1 T github.com/foo/a.X"}),
	)
	if h := tree.Nodes["github.com/foo/a/X"].Heat; h <= 0.9999 || h > 1 {
		t.Errorf("10MB -> 1B heat: got %f, want within (0.9999, 1]", h)
	}

	// 10MB -> deleted: exactly max shrinkage
	tree, _ = converter.SymtabDiffToTreemap(
		parseLines(t, []string{" 10000000 T github.com/foo/a.X"}),
		symtab.SymtabFile{},
	)
	if h := tree.Nodes["github.com/foo/a/X"].Heat; !approxEqual(h, 1) {
		t.Errorf("10MB -> deleted heat: got %f, want 1", h)
	}

	// 3x growth is beyond the +100% bound: same saturated color as added
	tree, _ = converter.SymtabDiffToTreemap(
		parseLines(t, []string{" 10 T github.com/foo/a.X"}),
		parseLines(t, []string{" 30 T github.com/foo/a.X"}),
	)
	if h := tree.Nodes["github.com/foo/a/X"].Heat; !approxEqual(h, 0) {
		t.Errorf("3x growth heat: got %f, want 0 (clamped at bound)", h)
	}
}

func TestDiffZeroAndEmpty(t *testing.T) {
	converter := DiffSymtabConverter{}

	// zero-size symbol in both versions: ignored entirely
	tree, stats := converter.SymtabDiffToTreemap(
		parseLines(t, []string{" 0 T github.com/foo/a.X"}),
		parseLines(t, []string{" 0 T github.com/foo/a.X"}),
	)
	if len(tree.Nodes) != 0 {
		t.Errorf("zero-zero symbol must not produce nodes, got %+v", tree.Nodes)
	}
	if stats != (DiffStats{}) {
		t.Errorf("zero-zero symbol must not affect stats, got %+v", stats)
	}

	// both versions empty
	tree, stats = converter.SymtabDiffToTreemap(symtab.SymtabFile{}, symtab.SymtabFile{})
	if len(tree.Nodes) != 0 || stats != (DiffStats{}) {
		t.Errorf("empty diff must be empty, got %+v %+v", tree, stats)
	}

	// everything deleted: deleted symbols still get minimal visible size
	tree, stats = converter.SymtabDiffToTreemap(
		parseLines(t, []string{" 100 T github.com/foo/a.X"}),
		symtab.SymtabFile{},
	)
	if stats.DeletedSymbols != 1 || stats.DeletedBytes != 100 {
		t.Errorf("wrong deletion stats: %+v", stats)
	}
	if got := tree.Nodes["github.com/foo/a/X"].Size; got < minDeletedSymbolSize {
		t.Errorf("deleted symbol size %f below minimum %f", got, minDeletedSymbolSize)
	}
}

func TestDiffTruncatedInput(t *testing.T) {
	// truncated and malformed lines are skipped by the parser;
	// the diff is still produced from the valid prefix
	base := parseLines(t, []string{
		" 100 T github.com/foo/a.KeepSame",
		" 200 T github.com/foo/a.Grow",
		" 30",                       // truncated: no symbol type
		" T github.com/foo/a.Ghost", // truncated: no size
		" xx T github.com/foo/a.BadSize",
	})
	target := parseLines(t, []string{
		" 100 T github.com/foo/a.KeepSame",
		" 500 T github.com/foo/a.Grow",
		" 4", // truncated mid-stream
	})

	converter := DiffSymtabConverter{}
	tree, stats := converter.SymtabDiffToTreemap(base, target)

	if stats.ChangedSymbols != 1 || stats.UnchangedSymbols != 1 {
		t.Errorf("truncated input: wrong stats %+v", stats)
	}
	if got := tree.Nodes["github.com/foo/a/Grow"].Size; !approxEqual(got, 500) {
		t.Errorf("truncated input: Grow size got %f, want 500", got)
	}
	for _, absent := range []string{"github.com/foo/a/Ghost", "github.com/foo/a/BadSize"} {
		if _, ok := tree.Nodes[absent]; ok {
			t.Errorf("truncated input: node %q must not be present", absent)
		}
	}
}

func TestDiffMaxDepth(t *testing.T) {
	base := parseLines(t, diffFixtureBaseLines())
	target := parseLines(t, diffFixtureTargetLines())

	converter := DiffSymtabConverter{MaxDepth: 2}
	tree, _ := converter.SymtabDiffToTreemap(base, target)

	if len(tree.Nodes) != 2 {
		t.Fatalf("max-depth=2: expected 2 nodes, got %d: %+v", len(tree.Nodes), tree.Nodes)
	}
	node, ok := tree.Nodes["github.com/foo"]
	if !ok {
		t.Fatal("max-depth=2: missing aggregated node github.com/foo")
	}
	wantSize := 2170 + 2*2170.0*deletedSymbolSizeFraction
	if !approxEqual(node.Size, wantSize) {
		t.Errorf("max-depth=2: size got %f, want %f", node.Size, wantSize)
	}
	wantHeat := 0.5 - 0.5*(2170.0-1620.0)/1620.0
	if !approxEqual(node.Heat, wantHeat) {
		t.Errorf("max-depth=2: heat got %f, want %f", node.Heat, wantHeat)
	}
}

func TestHeatForChangeBounds(t *testing.T) {
	cases := []struct {
		old, new float64
		want     float64
	}{
		{0, 0, 0.5},
		{0, 100, 0}, // added
		{100, 0, 1}, // deleted
		{100, 100, 0.5},
		{100, 200, 0}, // +100%: at the bound
		{100, 50, 0.75},
		{100, 10000, 0},     // growth beyond bound: clamped
		{10000, 1, 0.99995}, // shrinkage asymptotes to -100%
		{10000, 0, 1},       // deleted: exactly max shrinkage
	}
	for _, c := range cases {
		if got := heatForChange(c.old, c.new); !approxEqual(got, c.want) {
			t.Errorf("heatForChange(%v, %v): got %f, want %f", c.old, c.new, got, c.want)
		}
	}
}
