package trace

import "testing"

func TestPickSymbolForFile(t *testing.T) {
	syms := []Symbol{
		{Name: "simulate", Kind: KindFunction, File: "tools/derive_unlock_costs.py", Line: 107},
		{Name: "simulate", Kind: KindFunction, File: "addons/gut/gut_to_move.gd", Line: 72},
	}

	picked := PickSymbolForFile(syms, "addons/gut/gut_to_move.gd")
	if picked == nil || picked.File != "addons/gut/gut_to_move.gd" {
		t.Fatalf("picked = %v, want the gd definition", picked)
	}

	picked = PickSymbolForFile(syms, "nowhere.gd")
	if picked == nil || picked.File != "tools/derive_unlock_costs.py" {
		t.Fatalf("fallback picked = %v, want first definition", picked)
	}

	if PickSymbolForFile(nil, "x.gd") != nil {
		t.Fatal("empty input must return nil")
	}
}
