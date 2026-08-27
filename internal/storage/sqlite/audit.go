package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const auditColumns = `id, actor_user_id, actor_role, action, object_type, object_id, result, request_id, detail, occurred_at`

var auditSortColumns = map[string]string{
	"occurred_at": "occurred_at",
	"action":      "action",
	"id":          "id",
}

// AppendAuditEvent writes one immutable trail entry. It participates in the
// caller's transaction, so an audit failure rolls the business write back.
func (d *dataset) AppendAuditEvent(ctx context.Context, event domain.AuditEvent) (domain.AuditEvent, error) {
	if err := event.Validate(); err != nil {
		return domain.AuditEvent{}, err
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO audit_events
            (actor_user_id, actor_role, action, object_type, object_id, result, request_id, detail, occurred_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableInt64(event.ActorUserID), event.ActorRole, string(event.Action), event.ObjectType,
		event.ObjectID, string(event.Result), event.RequestID, event.Detail, formatTime(event.OccurredAt))
	if err != nil {
		return domain.AuditEvent{}, fmt.Errorf("insert audit event %s: %w", event.Action, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.AuditEvent{}, fmt.Errorf("read inserted audit event id: %w", err)
	}
	event.ID = id
	return event, nil
}

// ListAuditEvents runs the filtered, paginated trail query.
func (d *dataset) ListAuditEvents(ctx context.Context, filter domain.AuditFilter) (domain.PageResult[domain.AuditEvent], error) {
	page, err := filter.Page.Normalize(auditSortColumns, "occurred_at")
	if err != nil {
		return domain.PageResult[domain.AuditEvent]{}, err
	}
	var (
		conditions []string
		args       []any
	)
	if filter.ActorUserID != nil {
		conditions = append(conditions, "actor_user_id = ?")
		args = append(args, *filter.ActorUserID)
	}
	if action := strings.TrimSpace(filter.Action); action != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, action)
	}
	if objectType := strings.TrimSpace(filter.ObjectType); objectType != "" {
		conditions = append(conditions, "object_type = ?")
		args = append(args, objectType)
	}
	if objectID := strings.TrimSpace(filter.ObjectID); objectID != "" {
		conditions = append(conditions, "object_id = ?")
		args = append(args, objectID)
	}
	if filter.Since != nil {
		conditions = append(conditions, "occurred_at >= ?")
		args = append(args, formatTime(*filter.Since))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := countRows(ctx, d.q, `SELECT COUNT(*) FROM audit_events`+where, args...)
	if err != nil {
		return domain.PageResult[domain.AuditEvent]{}, err
	}

	query := `SELECT ` + auditColumns + ` FROM audit_events` + where +
		fmt.Sprintf(" ORDER BY %s %s, id DESC LIMIT ? OFFSET ?",
			auditSortColumns[page.SortBy], strings.ToUpper(string(page.Order)))
	args = append(args, page.Size, page.Offset())

	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.PageResult[domain.AuditEvent]{}, fmt.Errorf("list audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.AuditEvent, 0, page.Size)
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return domain.PageResult[domain.AuditEvent]{}, fmt.Errorf("scan audit event: %w", err)
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return domain.PageResult[domain.AuditEvent]{}, fmt.Errorf("iterate audit events: %w", err)
	}
	return domain.NewPageResult(items, total, page), nil
}

func scanAuditEvent(row rowScanner) (domain.AuditEvent, error) {
	var (
		event      domain.AuditEvent
		actorID    sql.NullInt64
		action     string
		result     string
		occurredAt string
	)
	if err := row.Scan(&event.ID, &actorID, &event.ActorRole, &action, &event.ObjectType,
		&event.ObjectID, &result, &event.RequestID, &event.Detail, &occurredAt); err != nil {
		return domain.AuditEvent{}, err
	}
	event.ActorUserID = readNullableInt64(actorID)
	event.Action = domain.AuditAction(action)
	event.Result = domain.AuditResult(result)

	parsed, err := parseTime(occurredAt)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	event.OccurredAt = parsed
	return event, nil
}
