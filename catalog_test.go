package main

import "testing"

func TestCatalogParseAndSearch(t *testing.T) {
	c := newModelCatalog("")
	raw := []byte(`{
  "acme": {"name": "Acme AI", "models": {"acme/one": {"name": "Acme One"}}},
  "hetzner": {"name": "Hetzner", "models": {"Qwen/Qwen3-27B": {"name": "Qwen3-27B"}}},
  "empty": {"name": "Empty Co", "models": {}}
}`)
	if err := c.parse(raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.provs) != 2 {
		t.Fatalf("providers with zero models should be dropped; got %d", len(c.provs))
	}

	provs, err := c.searchProvs("qwen")
	if err != nil {
		t.Fatalf("searchProvs: %v", err)
	}
	if len(provs) != 1 || provs[0].ID != "hetzner" {
		t.Fatalf("expected only hetzner to match qwen, got %+v", provs)
	}

	mods, known, err := c.modelsOf("hetzner", "qwen")
	if err != nil || !known {
		t.Fatalf("modelsOf: known=%v err=%v", known, err)
	}
	if len(mods) != 1 || mods[0].ID != "Qwen/Qwen3-27B" {
		t.Fatalf("unexpected models: %+v", mods)
	}

	mods2, known2, _ := c.modelsOf("hetzner", "llama")
	if !known2 || len(mods2) != 0 {
		t.Fatalf("filter llama on hetzner should be empty, got %+v (known=%v)", mods2, known2)
	}
}
