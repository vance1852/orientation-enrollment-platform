// Package migrations exposes the versioned schema as embedded SQL files.
//
// Every file is named <version>_<name>.sql and is applied exactly once. The
// checksum of each applied file is recorded so a repository that diverges from
// an already migrated database is reported instead of silently reapplied.
package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed sql/*.sql
var files embed.FS

// Migration is one versioned schema step.
type Migration struct {
	Version  int
	Name     string
	Script   string
	Checksum string
}

// All returns every embedded migration ordered by ascending version.
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(files, "sql")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseName(entry.Name())
		if err != nil {
			return nil, err
		}
		if previous, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("duplicate migration version %d in %s and %s", version, previous, entry.Name())
		}
		seen[version] = entry.Name()

		raw, err := files.ReadFile(path.Join("sql", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(raw)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			Script:   string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no migrations were embedded")
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for i, migration := range migrations {
		if migration.Version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1, found %d at position %d",
				migration.Version, i+1)
		}
	}
	return migrations, nil
}

// LatestVersion returns the highest embedded migration version.
func LatestVersion() (int, error) {
	all, err := All()
	if err != nil {
		return 0, err
	}
	return all[len(all)-1].Version, nil
}

func parseName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("migration %q must be named <version>_<name>.sql", filename)
	}
	version, err := strconv.Atoi(parts[0])
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("migration %q has a non positive version prefix", filename)
	}
	if strings.TrimSpace(parts[1]) == "" {
		return 0, "", fmt.Errorf("migration %q is missing a name", filename)
	}
	return version, parts[1], nil
}
