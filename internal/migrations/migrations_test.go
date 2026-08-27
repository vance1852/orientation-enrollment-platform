package migrations_test

import (
	"strings"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/migrations"
)

func TestAllReturnsContiguousVersions(t *testing.T) {
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("reading migrations failed: %v", err)
	}
	if len(all) < 4 {
		t.Fatalf("expected at least four migrations, got %d", len(all))
	}
	for i, migration := range all {
		if migration.Version != i+1 {
			t.Fatalf("migration %d has version %d", i, migration.Version)
		}
		if strings.TrimSpace(migration.Name) == "" {
			t.Fatalf("migration %d has no name", migration.Version)
		}
		if strings.TrimSpace(migration.Script) == "" {
			t.Fatalf("migration %d has an empty script", migration.Version)
		}
		if len(migration.Checksum) != 64 {
			t.Fatalf("migration %d checksum = %q", migration.Version, migration.Checksum)
		}
	}
}

func TestChecksumsAreStableAndDistinct(t *testing.T) {
	first, err := migrations.All()
	if err != nil {
		t.Fatalf("reading migrations failed: %v", err)
	}
	second, err := migrations.All()
	if err != nil {
		t.Fatalf("re-reading migrations failed: %v", err)
	}
	seen := make(map[string]int, len(first))
	for i := range first {
		if first[i].Checksum != second[i].Checksum {
			t.Fatalf("checksum of migration %d is not stable", first[i].Version)
		}
		if previous, duplicate := seen[first[i].Checksum]; duplicate {
			t.Fatalf("migrations %d and %d share a checksum", previous, first[i].Version)
		}
		seen[first[i].Checksum] = first[i].Version
	}
}

func TestLatestVersionMatchesTheLastMigration(t *testing.T) {
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("reading migrations failed: %v", err)
	}
	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("reading the latest version failed: %v", err)
	}
	if latest != all[len(all)-1].Version {
		t.Fatalf("LatestVersion() = %d, want %d", latest, all[len(all)-1].Version)
	}
}

func TestSchemaCoversEveryBusinessTable(t *testing.T) {
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("reading migrations failed: %v", err)
	}
	var combined strings.Builder
	for _, migration := range all {
		combined.WriteString(migration.Script)
	}
	script := combined.String()

	tables := []string{
		"users", "sessions", "terms", "courses", "course_prerequisites", "course_sections",
		"section_meetings", "student_registrations", "academic_records", "enrollments",
		"idempotency_keys", "audit_events", "jobs",
	}
	for _, table := range tables {
		if !strings.Contains(script, "CREATE TABLE "+table+" (") {
			t.Fatalf("table %s is missing from the schema", table)
		}
	}
	if !strings.Contains(script, "REFERENCES users (id)") {
		t.Fatal("the schema must declare foreign keys onto users")
	}
	if !strings.Contains(script, "CREATE UNIQUE INDEX ux_enrollments_active_seat") {
		t.Fatal("the schema must guard a single active seat per student and section")
	}
}
