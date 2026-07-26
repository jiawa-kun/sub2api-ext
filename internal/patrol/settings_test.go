package patrol

import (
	"testing"

	"sub2api-ext/internal/config"
)

func TestFailThresholdNormalizeAndValidate(t *testing.T) {
	// zero / negative falls back to 1 (legacy behaviour)
	rt := normalizeRuntime(Runtime{FailThreshold: 0})
	if rt.FailThreshold != 1 {
		t.Fatalf("threshold = %d, want 1", rt.FailThreshold)
	}
	rt = normalizeRuntime(Runtime{FailThreshold: -3})
	if rt.FailThreshold != 1 {
		t.Fatalf("threshold = %d, want 1", rt.FailThreshold)
	}
	// clamped at 10
	rt = normalizeRuntime(Runtime{FailThreshold: 99})
	if rt.FailThreshold != 10 {
		t.Fatalf("threshold = %d, want 10", rt.FailThreshold)
	}
	// in-range value preserved
	rt = normalizeRuntime(Runtime{FailThreshold: 3})
	if rt.FailThreshold != 3 {
		t.Fatalf("threshold = %d, want 3", rt.FailThreshold)
	}
	if err := validateRuntime(rt); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// out-of-range rejected by validate
	bad := rt
	bad.FailThreshold = 0
	if err := validateRuntime(bad); err == nil {
		t.Fatal("threshold 0 should fail validation")
	}
}

func TestFromConfigCarriesFailThreshold(t *testing.T) {
	rt := fromConfig(config.PatrolConfig{FailThreshold: 2})
	if rt.FailThreshold != 2 {
		t.Fatalf("threshold = %d, want 2", rt.FailThreshold)
	}
}
