package codex

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"grin/internal/errs"
)

type tableRequirement struct {
	Name    string
	Columns []string
}

type databaseCandidate struct {
	name    string
	version uint64
}

func resolveStateDB(ctx context.Context, sqlite sqliteRunner, home string) (string, error) {
	return resolveVersionedDatabase(ctx, sqlite, home, "state_", []tableRequirement{{
		Name:    "threads",
		Columns: []string{"id", "cwd", "thread_source"},
	}})
}

func resolveThreadHistoryDB(ctx context.Context, sqlite sqliteRunner, home string) (string, error) {
	return resolveVersionedDatabase(ctx, sqlite, home, "thread_history_", []tableRequirement{
		{Name: "thread_turns", Columns: []string{"thread_id", "turn_id", "status", "final_agent_item_id"}},
		{Name: "thread_items", Columns: []string{"thread_id", "turn_id", "item_id", "item_type", "item_json"}},
	})
}

func resolveVersionedDatabase(ctx context.Context, sqlite sqliteRunner, home, prefix string, requirements []tableRequirement) (string, error) {
	if ctx.Err() != nil {
		return "", errs.New(errs.ErrCancelled, "Codex history lookup was cancelled", true)
	}
	canonicalHome, err := canonicalExistingDirectory(home)
	if err != nil {
		return "", errs.New(errs.ErrUnsupported, "Codex data root is unavailable", false)
	}
	entries, err := os.ReadDir(canonicalHome)
	if err != nil {
		return "", errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
	}
	if ctx.Err() != nil {
		return "", errs.New(errs.ErrCancelled, "Codex history lookup was cancelled", true)
	}
	candidates := make([]databaseCandidate, 0)
	for _, entry := range entries {
		version, ok := databaseVersion(entry.Name(), prefix)
		if ok {
			candidates = append(candidates, databaseCandidate{name: entry.Name(), version: version})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].version == candidates[j].version {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].version > candidates[j].version
	})
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return "", errs.New(errs.ErrCancelled, "Codex history lookup was cancelled", true)
		}
		path, eligible, err := codexDatabasePath(canonicalHome, candidate.name)
		if err != nil {
			return "", err
		}
		if !eligible {
			continue
		}
		for _, requirement := range requirements {
			if err := validateTable(ctx, sqlite, path, requirement.Name, requirement.Columns...); err != nil {
				return "", err
			}
		}
		return path, nil
	}
	if ctx.Err() != nil {
		return "", errs.New(errs.ErrCancelled, "Codex history lookup was cancelled", true)
	}
	return "", errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
}

func databaseVersion(name, prefix string) (uint64, bool) {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".sqlite") {
		return 0, false
	}
	suffix := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".sqlite")
	version, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != suffix {
		return 0, false
	}
	return version, true
}

func codexDatabasePath(home, name string) (string, bool, error) {
	path := filepath.Join(home, name)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
	}
	relative, err := filepath.Rel(home, real)
	if err != nil {
		return "", false, errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
	}
	if relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false, nil
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", false, errs.New(errs.ErrUnsupported, "Codex persisted history is unavailable", false)
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	return real, true, nil
}

func validateTable(ctx context.Context, sqlite sqliteRunner, path, table string, required ...string) error {
	rows, err := queryRows[struct {
		Name string `json:"name"`
	}](ctx, sqlite, path, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	columns := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		columns[row.Name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return errs.New(errs.ErrUnsupported, "Codex database schema is not supported", false)
		}
	}
	return nil
}

func validateMatchingUserMessageShapes(ctx context.Context, sqlite sqliteRunner, path, threadID, marker string) error {
	query := "SELECT COUNT(*) AS invalid FROM thread_items WHERE thread_id=" + sqlLiteral(threadID) + " AND item_type='userMessage' AND instr(item_json," + sqlLiteral(marker) + ")>0 AND CASE WHEN json_valid(item_json)=0 THEN 1 WHEN json_type(item_json)!='object' THEN 1 WHEN json_extract(item_json,'$.type')!='userMessage' THEN 1 WHEN json_type(item_json,'$.content')!='array' THEN 1 ELSE 0 END"
	rows, err := queryRows[struct {
		Invalid int `json:"invalid"`
	}](ctx, sqlite, path, query)
	if err != nil || len(rows) != 1 || rows[0].Invalid != 0 {
		return errs.New(errs.ErrUnsupported, "Codex user-message history has an unsupported shape", false)
	}
	return nil
}

func matchingTurnsQuery(threadID, marker string) string {
	safeItem := "CASE WHEN json_valid(ti.item_json)=0 THEN '{\"content\":[]}' WHEN json_type(ti.item_json)!='object' THEN '{\"content\":[]}' WHEN json_type(ti.item_json,'$.content')!='array' THEN '{\"content\":[]}' ELSE ti.item_json END"
	return "SELECT DISTINCT ti.turn_id FROM thread_items AS ti, json_each(" + safeItem + ",'$.content') AS part WHERE ti.thread_id=" + sqlLiteral(threadID) + " AND ti.item_type='userMessage' AND json_extract(part.value,'$.type')='text' AND json_type(part.value,'$.text')='text' AND instr(json_extract(part.value,'$.text')," + sqlLiteral(marker) + ")>0 LIMIT 2"
}

func finalItemQuery(threadID, turnID, itemID string, maxBytes int64) string {
	textExpr := "json_extract(item_json,'$.text')"
	textSize := "length(CAST(" + textExpr + " AS BLOB))"
	return "SELECT item_type, json_extract(item_json,'$.type') AS kind, json_type(item_json,'$.text') AS text_kind, " + textSize + " AS text_size, CASE WHEN " + textSize + "<=" + strconv.FormatInt(maxBytes, 10) + " THEN " + textExpr + " END AS text FROM thread_items WHERE thread_id=" + sqlLiteral(threadID) + " AND turn_id=" + sqlLiteral(turnID) + " AND item_id=" + sqlLiteral(itemID) + " LIMIT 1"
}

func queryRows[T any](ctx context.Context, sqlite sqliteRunner, path, query string) ([]T, error) {
	output, err := sqlite(ctx, path, query)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.New(errs.ErrCancelled, "Codex history lookup was cancelled", true)
		}
		return nil, errs.New(errs.ErrUnsupported, "Codex persisted history could not be read", false)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return []T{}, nil
	}
	var rows []T
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, errs.New(errs.ErrUnsupported, "Codex persisted history has an unsupported shape", false)
	}
	return rows, nil
}

func runSQLite(ctx context.Context, path, query string) ([]byte, error) {
	command := exec.CommandContext(ctx, "sqlite3", "-init", "/dev/null", "-readonly", "-json", path, query)
	command.Stderr = io.Discard
	return command.Output()
}

func sqlLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
