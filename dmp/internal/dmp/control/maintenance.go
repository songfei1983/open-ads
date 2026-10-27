package control

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DuplicateVersion is one definition version that more than one writer inserted.
// ClickHouse cannot reject the losing insert, so the winner is the row with the
// lowest revision id and the others are garbage.
type DuplicateVersion struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Version      uint64 `json:"version"`
	Rows         uint64 `json:"rows"`
	Keeper       string `json:"keeper_revision_id"`
}

// PruneReport summarises a duplicate sweep. DryRun counts what would be deleted
// without touching storage.
type PruneReport struct {
	DryRun         bool               `json:"dry_run"`
	Groups         []DuplicateVersion `json:"groups"`
	RowsToDelete   uint64             `json:"rows_to_delete"`
	DeletedRows    uint64             `json:"deleted_rows"`
	MutationsFired uint64             `json:"mutations_fired"`
}

// Maintenance inspects and repairs store-level debris. It is optional so a store
// implementation can support the definition API without it.
type Maintenance interface {
	// FindDuplicateVersions lists every version written by more than one writer.
	FindDuplicateVersions(ctx context.Context) ([]DuplicateVersion, error)
	// PruneDuplicateVersions keeps argMin(revision_id) per version and deletes
	// the losing rows. DryRun only counts.
	PruneDuplicateVersions(ctx context.Context, dryRun bool) (PruneReport, error)
}

// definitionTables describes the versioned definition tables for inspection.
type definitionTable struct {
	name     string
	idColumn string
}

var definitionTables = []definitionTable{
	{name: "dmp_namespace_version", idColumn: "namespace"},
	{name: "dmp_provider_version", idColumn: "provider_id"},
	{name: "dmp_segment_definition_version", idColumn: "segment_id"},
}

// FindDuplicateVersions returns the versions with more than one stored row.
func (s *ClickHouseStore) FindDuplicateVersions(ctx context.Context) ([]DuplicateVersion, error) {
	var duplicates []DuplicateVersion
	for _, table := range definitionTables {
		sql := fmt.Sprintf(`SELECT %s, version, count() AS rows, min(revision_id) AS keeper
			FROM %s WHERE tenant_id = ? GROUP BY %s, version HAVING rows > 1
			ORDER BY %s, version`, table.idColumn, table.name, table.idColumn, table.idColumn)
		rows, err := s.conn.Query(ctx, sql, s.tenantID)
		if err != nil {
			return nil, fmt.Errorf("%w: inspect %s: %v", ErrStoreUnavailable, table.name, err)
		}
		for rows.Next() {
			var (
				duplicate DuplicateVersion
				keeper    uuid.UUID
			)
			if err := rows.Scan(&duplicate.ResourceID, &duplicate.Version, &duplicate.Rows, &keeper); err != nil {
				rows.Close()
				return nil, fmt.Errorf("%w: scan %s duplicates: %v", ErrStoreUnavailable, table.name, err)
			}
			duplicate.ResourceType = resourceTypeOf(table.name)
			duplicate.Keeper = keeper.String()
			duplicates = append(duplicates, duplicate)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("%w: inspect %s: %v", ErrStoreUnavailable, table.name, err)
		}
		rows.Close()
	}
	return duplicates, nil
}

// PruneDuplicateVersions deletes the losing rows of every duplicated version.
func (s *ClickHouseStore) PruneDuplicateVersions(ctx context.Context, dryRun bool) (PruneReport, error) {
	duplicates, err := s.FindDuplicateVersions(ctx)
	if err != nil {
		return PruneReport{}, err
	}
	report := PruneReport{DryRun: dryRun, Groups: duplicates}
	for _, duplicate := range duplicates {
		// Every duplicated version keeps exactly one row.
		report.RowsToDelete += duplicate.Rows - 1
	}
	if dryRun {
		return report, nil
	}

	for _, duplicate := range duplicates {
		table, ok := tableOf(duplicate.ResourceType)
		if !ok {
			continue
		}
		keeper, err := uuid.Parse(duplicate.Keeper)
		if err != nil {
			return report, fmt.Errorf("parse keeper revision id %q: %v", duplicate.Keeper, err)
		}
		// mutations_sync waits for the delete so the caller sees a repaired store.
		sql := fmt.Sprintf(`ALTER TABLE %s DELETE WHERE tenant_id = ? AND %s = ? AND version = ? AND revision_id != ? SETTINGS mutations_sync = 1`,
			table.name, table.idColumn)
		if err := s.conn.Exec(ctx, sql, s.tenantID, duplicate.ResourceID, duplicate.Version, keeper); err != nil {
			return report, fmt.Errorf("%w: prune %s %q v%d: %v", ErrStoreUnavailable, table.name, duplicate.ResourceID, duplicate.Version, err)
		}
		report.MutationsFired++
		report.DeletedRows += duplicate.Rows - 1
	}
	return report, nil
}

func tableOf(resourceType string) (definitionTable, bool) {
	for _, table := range definitionTables {
		if resourceTypeOf(table.name) == resourceType {
			return table, true
		}
	}
	return definitionTable{}, false
}

func resourceTypeOf(table string) string {
	switch table {
	case "dmp_namespace_version":
		return "namespace"
	case "dmp_provider_version":
		return "provider"
	default:
		return "segment"
	}
}
