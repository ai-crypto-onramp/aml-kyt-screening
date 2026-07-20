package decision

import (
	"testing"
)

func TestDecide(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, UnknownDecision: DecisionManualReview, perChain: map[string]chainThreshold{}}
	cases := []struct {
		exposure string
		want     string
	}{
		{ExposureSanctioned, DecisionBlock},
		{ExposureHighRisk, DecisionManualReview},
		{ExposureClean, DecisionAllow},
		{ExposureUnknown, DecisionManualReview},
		{"unknown-exposure", DecisionManualReview},
	}
	for _, c := range cases {
		if got := th.Decide(c.exposure); got != c.want {
			t.Errorf("Decide(%q) = %q, want %q", c.exposure, got, c.want)
		}
	}
}

func TestDecideUnknownBlock(t *testing.T) {
	th := &Thresholds{UnknownDecision: DecisionBlock, perChain: map[string]chainThreshold{}}
	if got := th.Decide(ExposureUnknown); got != DecisionBlock {
		t.Errorf("Decide(unknown) with block config = %q, want %q", got, DecisionBlock)
	}
	// Invalid UnknownDecision value must fall back to manual_review.
	th.UnknownDecision = "weird"
	if got := th.Decide(ExposureUnknown); got != DecisionManualReview {
		t.Errorf("Decide(unknown) with invalid config = %q, want %q", got, DecisionManualReview)
	}
}

func TestExposureFromScoreGlobal(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, perChain: map[string]chainThreshold{}}
	cases := []struct {
		score int
		want  string
	}{
		{95, ExposureSanctioned},
		{90, ExposureSanctioned},
		{89, ExposureHighRisk},
		{50, ExposureHighRisk},
		{49, ExposureClean},
		{0, ExposureClean},
		{-1, ExposureUnknown},
	}
	for _, c := range cases {
		if got := th.ExposureFromScore(c.score, "ethereum"); got != c.want {
			t.Errorf("ExposureFromScore(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestExposureFromScorePerChainOverride(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, perChain: map[string]chainThreshold{}}
	th.SetChainOverride("bitcoin", 80, 40)
	if got := th.ExposureFromScore(82, "bitcoin"); got != ExposureSanctioned {
		t.Errorf("bitcoin override block: got %q, want %q", got, ExposureSanctioned)
	}
	if got := th.ExposureFromScore(82, "ethereum"); got != ExposureHighRisk {
		t.Errorf("ethereum default: got %q, want %q", got, ExposureHighRisk)
	}
	if got := th.ExposureFromScore(45, "bitcoin"); got != ExposureHighRisk {
		t.Errorf("bitcoin override high: got %q, want %q", got, ExposureHighRisk)
	}
}

func TestSeverityFor(t *testing.T) {
	cases := map[string]string{
		ExposureSanctioned: "critical",
		ExposureHighRisk:   "high",
		ExposureUnknown:    "medium",
		ExposureClean:      "low",
		"":                 "low",
	}
	for in, want := range cases {
		if got := SeverityFor(in); got != want {
			t.Errorf("SeverityFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecideVendorUnreachableNeverAllows(t *testing.T) {
	if got := DecideVendorUnreachable(); got == DecisionAllow {
		t.Fatal("vendor-unreachable must never return allow")
	}
}

func TestDefaultThresholdsFromEnv(t *testing.T) {
	t.Setenv("BLOCK_THRESHOLD", "80")
	t.Setenv("HIGH_RISK_THRESHOLD", "40")
	t.Setenv("UNKNOWN_DECISION", "BLOCK")
	t.Setenv("BLOCK_THRESHOLD_BITCOIN", "70")
	t.Setenv("HIGH_RISK_THRESHOLD_BITCOIN", "30")
	th := DefaultThresholds()
	if got := th.ExposureFromScore(85, "ethereum"); got != ExposureSanctioned {
		t.Errorf("ethereum score 85 with BLOCK_THRESHOLD=80: got %q", got)
	}
	if got := th.ExposureFromScore(72, "bitcoin"); got != ExposureSanctioned {
		t.Errorf("bitcoin override 72: got %q", got)
	}
	if got := th.Decide(ExposureUnknown); got != DecisionBlock {
		t.Errorf("UnknownDecision=block: got %q", got)
	}
}

func TestReloadFromEnv(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, perChain: map[string]chainThreshold{}}
	t.Setenv("BLOCK_THRESHOLD", "75")
	th.ReloadFromEnv()
	if got := th.ExposureFromScore(76, "ethereum"); got != ExposureSanctioned {
		t.Errorf("after reload score 76: got %q, want %q", got, ExposureSanctioned)
	}
}

func TestNewThresholdsInvalidUnknownDecision(t *testing.T) {
	th := NewThresholds(90, 50, "weird")
	if th.UnknownDecision != DecisionManualReview {
		t.Errorf("invalid unknown decision should fall back to manual_review, got %q", th.UnknownDecision)
	}
}

func TestNewThresholdsBlockUnknownDecision(t *testing.T) {
	th := NewThresholds(90, 50, DecisionBlock)
	if th.UnknownDecision != DecisionBlock {
		t.Errorf("block unknown decision: got %q", th.UnknownDecision)
	}
}

func TestReloadFromEnvInvalidUnknownDecision(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, UnknownDecision: DecisionBlock, perChain: map[string]chainThreshold{}}
	t.Setenv("UNKNOWN_DECISION", "bogus")
	th.ReloadFromEnv()
	if th.UnknownDecision != DecisionManualReview {
		t.Errorf("invalid unknown decision after reload: got %q", th.UnknownDecision)
	}
}

func TestReloadFromEnvIgnoresNonNumeric(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, perChain: map[string]chainThreshold{}}
	t.Setenv("BLOCK_THRESHOLD_BITCOIN", "not-a-number")
	t.Setenv("HIGH_RISK_THRESHOLD_BITCOIN", "also-not-a-number")
	th.ReloadFromEnv()
	if got := th.ExposureFromScore(95, "bitcoin"); got != ExposureSanctioned {
		t.Errorf("bitcoin with global default: got %q want %q", got, ExposureSanctioned)
	}
}

func TestReloadFromEnvHandlesMalformedEnv(t *testing.T) {
	th := &Thresholds{BlockThreshold: 90, HighRiskThreshold: 50, perChain: map[string]chainThreshold{}}
	t.Setenv("BLOCK_THRESHOLD_ETHEREUM", "70")
	// Inject a malformed env entry (no '='). os.Environ returns "KEY=VALUE";
	// we cannot directly inject a malformed one, but ReloadFromEnv handles
	// missing '=' by skipping. Just verify it doesn't panic.
	th.ReloadFromEnv()
	if got := th.ExposureFromScore(75, "ethereum"); got != ExposureSanctioned {
		t.Errorf("ethereum 75: got %q", got)
	}
}

func TestEnvIntFallsBack(t *testing.T) {
	t.Setenv("BLOCK_THRESHOLD", "not-a-number")
	if got := envInt("BLOCK_THRESHOLD", 42); got != 42 {
		t.Errorf("envInt invalid: %d", got)
	}
}

func TestEnvOrReturnsValue(t *testing.T) {
	t.Setenv("UNKNOWN_DECISION", "BLOCK")
	if got := envOr("UNKNOWN_DECISION", "default"); got != "BLOCK" {
		t.Errorf("envOr: %q", got)
	}
}