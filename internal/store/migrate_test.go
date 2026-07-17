package store

import (
	"testing"
	"time"
)

func TestTTLFor(t *testing.T) {
	def := time.Hour
	san := 24 * time.Hour
	cases := []struct {
		exposure string
		want     time.Duration
	}{
		{"SANCTIONED", san},
		{"CLEAN", def},
		{"UNKNOWN", def},
		{"HIGH_RISK", def},
		{"", def},
	}
	for _, c := range cases {
		if got := TTLFor(c.exposure, def, san); got != c.want {
			t.Errorf("TTLFor(%q) = %s, want %s", c.exposure, got, c.want)
		}
	}
}

func TestLoadMigrations(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if len(migrations) != 5 {
		t.Fatalf("expected 5 migration pairs, got %d", len(migrations))
	}
	want := []int{1, 2, 3, 4, 5}
	for i, m := range migrations {
		if m.Version != want[i] {
			t.Errorf("migration %d: version = %d, want %d", i, m.Version, want[i])
		}
		if m.Up == "" {
			t.Errorf("migration %d: missing Up script", m.Version)
		}
		if m.Down == "" {
			t.Errorf("migration %d: missing Down script", m.Version)
		}
	}
}

func TestLoadMigrationsContainsExpectedTables(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	wantTables := []string{
		"address_risk_cache",
		"kyt_screens",
		"kyt_alerts",
		"vendor_responses",
		"audit_events",
	}
	for _, table := range wantTables {
		found := false
		for _, m := range migrations {
			if contains(m.Up, table) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no migration creates table %q", table)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}