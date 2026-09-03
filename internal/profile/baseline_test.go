package profile

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedBaselineIsCurrentFullSignature(t *testing.T) {
	var sig Signature
	if err := json.Unmarshal(EmbeddedBaseline(), &sig); err != nil {
		t.Fatalf("decode embedded baseline: %v", err)
	}
	if sig.Version != SignatureVersion {
		t.Fatalf("embedded baseline version = %d, want %d", sig.Version, SignatureVersion)
	}
	if sig.Coverage.Sampled {
		t.Fatal("embedded baseline was generated from a sampled scan")
	}
	if sig.Coverage.Files == 0 || sig.Coverage.ReadBytes != sig.Coverage.FileBytes {
		t.Fatalf("embedded baseline coverage = %+v, want a non-empty full scan", sig.Coverage)
	}
}
