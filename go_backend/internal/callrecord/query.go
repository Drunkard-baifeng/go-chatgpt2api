package callrecord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInput = errors.New("日志查询参数无效")
var ErrNotFound = errors.New("调用记录不存在")

type Filter struct {
	Type, StartDate, EndDate, Status, Endpoint, Model, Account, ConversationID, Search string
	Limit, Offset                                                                      int
}
type Page struct {
	Items         [][]byte
	Total         int64
	Limit, Offset int
	Facets        map[string]map[string]int64
	Stats         map[string]int64
}

func (f Filter) Validate() error {
	if f.Limit < 1 || f.Limit > 20000 || f.Offset < 0 || f.Offset > 1000000 {
		return ErrInput
	}
	for _, value := range []string{f.Type, f.Endpoint, f.Model, f.ConversationID, f.Search} {
		if len(value) > 512 || strings.ContainsRune(value, 0) {
			return ErrInput
		}
	}
	if f.Account != "" && safeAccountEmail(f.Account) != f.Account {
		return ErrInput
	}
	for _, value := range []string{f.StartDate, f.EndDate} {
		if value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return ErrInput
			}
		}
	}
	if f.StartDate != "" && f.EndDate != "" && f.StartDate > f.EndDate {
		return ErrInput
	}
	switch f.Status {
	case "", "success", "failed", "rate_limited", "text_review", "partial_success", "unknown", "running":
	default:
		return ErrInput
	}
	return nil
}
func validID(id string) bool {
	return strings.TrimSpace(id) == id && id != "" && len(id) <= 64 && !strings.ContainsAny(id, "\x00\r\n")
}
func (f Filter) where() (string, []any) {
	conditions := []string{"TRUE"}
	args := []any{}
	add := func(column, op string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s %s $%d", column, op, len(args)))
	}
	for _, item := range []struct{ column, value string }{{"type", f.Type}, {"outcome", f.Status}, {"endpoint", f.Endpoint}, {"model", f.Model}, {"account_email", f.Account}, {"conversation_id", f.ConversationID}} {
		if item.value != "" {
			add(item.column, "=", item.value)
		}
	}
	if f.StartDate != "" {
		add("event_day", ">=", f.StartDate)
	}
	if f.EndDate != "" {
		add("event_day", "<=", f.EndDate)
	}
	if f.Search != "" {
		args = append(args, strings.ToLower(f.Search))
		conditions = append(conditions, fmt.Sprintf("strpos(search_text,$%d)>0", len(args)))
	}
	return strings.Join(conditions, " AND "), args
}
func (r *Repository) List(parent context.Context, f Filter) (Page, error) {
	p := Page{Limit: f.Limit, Offset: f.Offset, Items: [][]byte{}, Facets: map[string]map[string]int64{}, Stats: map[string]int64{"total": 0, "success": 0, "text_review": 0, "failed": 0, "limited": 0, "image": 0}}
	if err := f.Validate(); err != nil {
		return p, err
	}
	if r.db == nil {
		return p, ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return p, storageError(ctx, err)
	}
	defer tx.Rollback()
	where, args := f.where()
	err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE outcome IN ('success','partial_success')),count(*) FILTER(WHERE outcome='text_review'),count(*) FILTER(WHERE outcome='failed'),count(*) FILTER(WHERE outcome='rate_limited'),count(*) FILTER(WHERE business_kind IN ('image_generation','image_edit','image_chat')) FROM call_records WHERE `+where, args...).Scan(&p.Total, ptr(p.Stats, "success"), ptr(p.Stats, "text_review"), ptr(p.Stats, "failed"), ptr(p.Stats, "limited"), ptr(p.Stats, "image"))
	if err != nil {
		return p, storageError(ctx, err)
	}
	p.Stats["total"] = p.Total
	for key, column := range map[string]string{"statuses": "outcome", "endpoints": "endpoint", "models": "model", "accounts": "account_email"} {
		rows, e := tx.QueryContext(ctx, "SELECT "+column+",count(*) FROM call_records WHERE "+where+" GROUP BY "+column+" ORDER BY count(*) DESC,"+column+" LIMIT 1000", args...)
		if e != nil {
			return p, storageError(ctx, e)
		}
		facet := map[string]int64{}
		for rows.Next() {
			var name string
			var count int64
			if e = rows.Scan(&name, &count); e != nil {
				rows.Close()
				return p, ErrStorage
			}
			if name != "" {
				facet[name] = count
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return p, storageError(ctx, e)
		}
		p.Facets[key] = facet
	}
	queryArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT payload FROM call_records WHERE %s ORDER BY sequence DESC LIMIT $%d OFFSET $%d", where, len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return p, storageError(ctx, err)
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return p, ErrStorage
		}
		if len(raw) > 2<<20 {
			rows.Close()
			return p, ErrStorage
		}
		p.Items = append(p.Items, raw)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, storageError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return p, storageError(ctx, err)
	}
	return p, nil
}

// statScan 将 database/sql 的整数扫描回显式统计表。
type statScan struct {
	target map[string]int64
	key    string
}

func ptr(target map[string]int64, key string) *statScan { return &statScan{target, key} }
func (s *statScan) Scan(value any) error {
	switch n := value.(type) {
	case int64:
		s.target[s.key] = n
	default:
		return ErrStorage
	}
	return nil
}

func (r *Repository) Get(parent context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, ErrInput
	}
	if r.db == nil {
		return nil, ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	var raw []byte
	err := r.db.QueryRowContext(ctx, "SELECT payload FROM call_records WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, storageError(ctx, err)
	}
	if len(raw) > 2<<20 {
		return nil, ErrStorage
	}
	return raw, nil
}
func (r *Repository) Delete(parent context.Context, ids []string) (int64, error) {
	if err := ValidateIDs(ids); err != nil {
		return 0, err
	}
	if r.db == nil {
		return 0, ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	// 活动调用的初始记录必须保留到终态保存，删除不会改变指标或世代。
	result, err := r.db.ExecContext(ctx, "DELETE FROM call_records WHERE id=ANY($1) AND outcome<>'running'", ids)
	if err != nil {
		return 0, storageError(ctx, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, ErrStorage
	}
	return count, nil
}
func ValidateIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > 1000 {
		return ErrInput
	}
	for _, id := range ids {
		if !validID(id) {
			return ErrInput
		}
	}
	return nil
}
