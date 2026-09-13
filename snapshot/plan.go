package snapshot

import ()

type TableImportMode string

const (
	TableImportSkip    TableImportMode = "skip"
	TableImportReplace TableImportMode = "replace"
	TableImportFiles   TableImportMode = "files"
)

type ImportPlan struct {
	Full   bool
	Reason string
	Tables []TableImportPlan
}

type ImportImpact string

const (
	ImportImpactNone    ImportImpact = "none"
	ImportImpactMerge   ImportImpact = "merge"
	ImportImpactReplace ImportImpact = "replace"
)

type TableImportPlan struct {
	Table  TableManifest
	Mode   TableImportMode
	Files  []FileManifest
	Reason string
}

func PlanIncrementalImport(previous, current Manifest) ImportPlan {
	return planIncrementalImport(previous, current, false)
}

// PlanMergeImport prepares a monotonic cache refresh. Changed snapshot files
// are upserted without deleting rows that disappeared from the snapshot. Use
// PlanIncrementalImport when the destination must exactly mirror the source.
func PlanMergeImport(previous, current Manifest) ImportPlan {
	return planIncrementalImport(previous, current, true)
}

func planIncrementalImport(previous, current Manifest, merge bool) ImportPlan {
	if current.Version != previous.Version {
		return ImportPlan{Full: true, Reason: "manifest version changed"}
	}
	previousTables := make(map[string]TableManifest, len(previous.Tables))
	for _, table := range previous.Tables {
		previousTables[table.Name] = table
	}
	currentTables := make(map[string]TableManifest, len(current.Tables))
	for _, table := range current.Tables {
		currentTables[table.Name] = table
	}
	for name := range previousTables {
		if _, ok := currentTables[name]; !ok {
			return ImportPlan{Full: true, Reason: "table removed: " + name}
		}
	}
	plan := ImportPlan{}
	for _, table := range current.Tables {
		previousTable, ok := previousTables[table.Name]
		if !ok {
			mode := TableImportReplace
			reason := "new table"
			if merge {
				mode = TableImportFiles
				reason = "merge new table"
			}
			plan.Tables = append(plan.Tables, TableImportPlan{
				Table:  table,
				Mode:   mode,
				Files:  tableFileManifests(table),
				Reason: reason,
			})
			continue
		}
		tablePlan := planTableIncrement(previousTable, table, merge)
		plan.Tables = append(plan.Tables, tablePlan)
	}
	return plan
}

func (p ImportPlan) Changed() bool {
	if p.Full {
		return true
	}
	for _, table := range p.Tables {
		if table.Mode != TableImportSkip {
			return true
		}
	}
	return false
}

func (p ImportPlan) Impact() ImportImpact {
	if p.Full {
		return ImportImpactReplace
	}
	impact := ImportImpactNone
	for _, table := range p.Tables {
		switch table.Mode {
		case TableImportReplace:
			return ImportImpactReplace
		case TableImportFiles:
			impact = ImportImpactMerge
		}
	}
	return impact
}

func planTableIncrement(previous, current TableManifest, merge bool) TableImportPlan {
	if !sameStrings(previous.Columns, current.Columns) {
		return TableImportPlan{Table: current, Mode: TableImportReplace, Files: tableFileManifests(current), Reason: "columns changed"}
	}
	previousFiles := tableFileManifests(previous)
	currentFiles := tableFileManifests(current)
	if len(previousFiles) == 0 && len(currentFiles) == 0 {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	if !allFilesHaveFingerprints(previousFiles) || !allFilesHaveFingerprints(currentFiles) {
		return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "missing file fingerprints"}
	}
	if sameLogicalFileManifests(current.Name, previousFiles, currentFiles) {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	if merge {
		return planTableMerge(previousFiles, currentFiles, current)
	}
	if len(currentFiles) < len(previousFiles) {
		return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "files removed"}
	}
	for i := 0; i < len(previousFiles)-1; i++ {
		if !sameLogicalFileManifest(current.Name, previousFiles[i], currentFiles[i]) {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "non-tail file changed"}
		}
	}
	changed := make([]FileManifest, 0, len(currentFiles)-len(previousFiles)+1)
	if len(previousFiles) > 0 {
		oldTail := previousFiles[len(previousFiles)-1]
		newTail := currentFiles[len(previousFiles)-1]
		if logicalFileKey(current.Name, oldTail.Path) != logicalFileKey(current.Name, newTail.Path) {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "tail path changed"}
		}
		if !sameLogicalFileManifest(current.Name, oldTail, newTail) {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "tail file changed"}
		}
	}
	for i := len(previousFiles); i < len(currentFiles); i++ {
		changed = append(changed, currentFiles[i])
	}
	if len(changed) == 0 {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	return TableImportPlan{Table: current, Mode: TableImportFiles, Files: changed, Reason: "tail files changed"}
}

func planTableMerge(previousFiles, currentFiles []FileManifest, current TableManifest) TableImportPlan {
	previousByPath := make(map[string]FileManifest, len(previousFiles))
	for _, file := range previousFiles {
		previousByPath[logicalFileKey(current.Name, file.Path)] = file
	}
	currentPaths := make(map[string]struct{}, len(currentFiles))
	changed := make([]FileManifest, 0, len(currentFiles))
	for _, file := range currentFiles {
		key := logicalFileKey(current.Name, file.Path)
		currentPaths[key] = struct{}{}
		previous, ok := previousByPath[key]
		if !ok || !sameLogicalFileManifest(current.Name, previous, file) {
			changed = append(changed, file)
		}
	}
	for _, file := range previousFiles {
		if _, ok := currentPaths[logicalFileKey(current.Name, file.Path)]; !ok {
			return TableImportPlan{Table: current, Mode: TableImportReplace, Files: currentFiles, Reason: "files removed"}
		}
	}
	if len(changed) == 0 {
		return TableImportPlan{Table: current, Mode: TableImportSkip, Reason: "unchanged"}
	}
	return TableImportPlan{Table: current, Mode: TableImportFiles, Files: changed, Reason: "merge changed files"}
}

func allFilesHaveFingerprints(files []FileManifest) bool {
	for _, file := range files {
		if file.Path == "" || file.SHA256 == "" {
			return false
		}
	}
	return true
}

func sameFileManifest(a, b FileManifest) bool {
	return a.Path == b.Path && a.Rows == b.Rows && a.Size == b.Size && a.SHA256 == b.SHA256
}

func fileManifestPaths(files []FileManifest) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func fileManifestRows(files []FileManifest) int {
	rows := 0
	for _, file := range files {
		rows += file.Rows
	}
	return rows
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
