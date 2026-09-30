// Package reminders adapts the CLI's CloudKit implementation for server use.
package reminders

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"icloud-reminders/internal/auth"
	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/cloudkit"
	syncengine "icloud-reminders/internal/sync"
	"icloud-reminders/internal/writer"
	"icloud-reminders/pkg/models"
)

// Error exposes only stable, deliberately public error messages.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func invalid(message string) error { return &Error{"invalid_argument", message} }

// PublicError prevents upstream response bodies, URLs and credentials from
// escaping through MCP errors or logs.
func PublicError(err error) *Error {
	var public *Error
	if errors.As(err, &public) {
		return public
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &Error{"request_timeout", "Request ended before completion. A write may already have succeeded; inspect the reminder before retrying."}
	}
	if errors.Is(err, auth.ErrAuthRequired) {
		return &Error{"auth_required", "Run reminders auth with the server's data directory, then retry."}
	}
	var api *cloudkit.APIError
	if errors.As(err, &api) && (api.StatusCode == 401 || api.StatusCode == 403) {
		return &Error{"auth_required", "Run reminders auth with the server's data directory, then retry."}
	}
	return &Error{"icloud_request_failed", "iCloud request failed. A write may already have succeeded; inspect the reminder before retrying."}
}

type ListInput struct {
	ListID           string `json:"list_id,omitempty" jsonschema:"Exact list ID returned by list_reminder_lists"`
	ParentID         string `json:"parent_id,omitempty" jsonschema:"Exact parent reminder ID; return only its direct subtasks"`
	Query            string `json:"query,omitempty" jsonschema:"Case-insensitive substring of the reminder title"`
	IncludeCompleted bool   `json:"include_completed,omitempty" jsonschema:"Also return completed reminders"`
	Limit            int    `json:"limit,omitempty" jsonschema:"Page size: 1 to 500; default 100"`
	Offset           int    `json:"offset,omitempty" jsonschema:"Number of matching reminders to skip; default 0"`
}

type IDInput struct {
	ID string `json:"id" jsonschema:"Exact reminder ID returned by list_reminders or create_reminder"`
}

type CreateInput struct {
	Title    string `json:"title" jsonschema:"Reminder title"`
	ListID   string `json:"list_id" jsonschema:"Exact list ID returned by list_reminder_lists"`
	Due      string `json:"due,omitempty" jsonschema:"Due date in YYYY-MM-DD format"`
	Priority string `json:"priority,omitempty" jsonschema:"none, low, medium or high; default none"`
	Notes    string `json:"notes,omitempty" jsonschema:"Optional notes"`
	ParentID string `json:"parent_id,omitempty" jsonschema:"Exact parent reminder ID in the same list"`
}

type UpdateInput struct {
	ID       string `json:"id" jsonschema:"Exact reminder ID"`
	Title    string `json:"title,omitempty" jsonschema:"New nonempty title; omitted fields remain unchanged"`
	Due      string `json:"due,omitempty" jsonschema:"New due date in YYYY-MM-DD format; clearing a date is unsupported"`
	Priority string `json:"priority,omitempty" jsonschema:"none, low, medium or high"`
	Notes    string `json:"notes,omitempty" jsonschema:"New nonempty notes; clearing notes is unsupported"`
}

type DeleteInput struct {
	ID      string `json:"id" jsonschema:"Exact reminder ID"`
	Confirm bool   `json:"confirm" jsonschema:"Must be true to permanently delete this reminder"`
}

type SyncInput struct {
	Full bool `json:"full,omitempty" jsonschema:"Discard the local delta token and perform a full sync"`
}

type ListsResult struct {
	Lists []*models.ReminderList `json:"lists"`
}
type ReminderResult struct {
	Reminder *models.Reminder `json:"reminder"`
}
type MutationResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type SyncResult struct {
	Reminders int `json:"reminders"`
	Lists     int `json:"lists"`
}
type Page struct {
	Reminders  []*models.Reminder `json:"reminders"`
	Total      int                `json:"total"`
	NextOffset *int               `json:"next_offset,omitempty"`
}

// Backend is the narrow contract consumed by the MCP layer.
type Backend interface {
	ListLists(context.Context) (ListsResult, error)
	List(context.Context, ListInput) (Page, error)
	Get(context.Context, IDInput) (ReminderResult, error)
	Create(context.Context, CreateInput) (MutationResult, error)
	Update(context.Context, UpdateInput) (MutationResult, error)
	Complete(context.Context, IDInput) (MutationResult, error)
	Delete(context.Context, DeleteInput) (MutationResult, error)
	Sync(context.Context, SyncInput) (SyncResult, error)
}

// Service owns one account. All sync/read/write operations are serialized;
// queued calls can still be canceled while waiting for the account.
type Service struct {
	directory string
	timeout   time.Duration
	gate      chan struct{}
}

func New(directory string, timeout time.Duration) *Service {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Service{directory: directory, timeout: timeout, gate: gate}
}

func (s *Service) run(ctx context.Context, full bool, operation func(*syncengine.Engine, *writer.Writer) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return PublicError(ctx.Err())
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return PublicError(err)
	}
	sessionFile := filepath.Join(s.directory, "session.json")
	session, err := auth.NewNonInteractive(ctx).EnsureSession(sessionFile, false)
	if err != nil {
		if ctx.Err() != nil {
			return PublicError(ctx.Err())
		}
		return PublicError(err)
	}
	client, err := cloudkit.NewFromSession(session)
	if err != nil {
		return PublicError(err)
	}
	engine := syncengine.NewWithCache(client.WithContext(ctx), sessionFile, cache.LoadFrom(s.directory))
	engine.NonInteractive = true
	// A directory accidentally reused after authenticating another account
	// must never reuse the previous account's records or delta token.
	ownerID, err := engine.CK.GetOwnerID()
	if err != nil {
		return PublicError(err)
	}
	if engine.Cache.OwnerID != nil && *engine.Cache.OwnerID != ownerID {
		engine.Cache = engine.Cache.Reset()
	}
	engine.Cache.OwnerID = &ownerID
	if err := engine.Sync(full); err != nil {
		return PublicError(err)
	}
	if err := operation(engine, writer.New(engine.CK, engine)); err != nil {
		if ctx.Err() != nil {
			return PublicError(ctx.Err())
		}
		return PublicError(err)
	}
	return nil
}

func (s *Service) ListLists(ctx context.Context) (out ListsResult, err error) {
	out.Lists = []*models.ReminderList{}
	err = s.run(ctx, false, func(engine *syncengine.Engine, _ *writer.Writer) error {
		out.Lists = append(out.Lists, engine.GetLists()...)
		sort.Slice(out.Lists, func(i, j int) bool { return out.Lists[i].ID < out.Lists[j].ID })
		return nil
	})
	return
}

func (s *Service) List(ctx context.Context, in ListInput) (out Page, err error) {
	if in.Limit == 0 {
		in.Limit = 100
	}
	if in.Limit < 1 || in.Limit > 500 || in.Offset < 0 {
		return out, invalid("limit must be 1..500 and offset must be nonnegative")
	}
	out.Reminders = []*models.Reminder{}
	err = s.run(ctx, false, func(engine *syncengine.Engine, _ *writer.Writer) error {
		if in.ListID != "" {
			if err := exactList(engine, in.ListID); err != nil {
				return err
			}
		}
		if in.ParentID != "" {
			if err := exactReminder(engine, in.ParentID); err != nil {
				return err
			}
		}
		matches := []*models.Reminder{}
		for _, item := range engine.GetReminders(in.IncludeCompleted) {
			if in.ListID != "" && (item.ListRef == nil || *item.ListRef != in.ListID) {
				continue
			}
			if in.ParentID != "" && (item.ParentRef == nil || *item.ParentRef != in.ParentID) {
				continue
			}
			if in.Query != "" && !strings.Contains(strings.ToLower(item.Title), strings.ToLower(in.Query)) {
				continue
			}
			matches = append(matches, item)
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		out.Total = len(matches)
		if in.Offset >= len(matches) {
			return nil
		}
		end := min(in.Offset+in.Limit, len(matches))
		out.Reminders = matches[in.Offset:end]
		if end < len(matches) {
			out.NextOffset = &end
		}
		return nil
	})
	return
}

func (s *Service) Get(ctx context.Context, in IDInput) (out ReminderResult, err error) {
	if strings.TrimSpace(in.ID) == "" {
		return out, invalid("id is required")
	}
	err = s.run(ctx, false, func(engine *syncengine.Engine, _ *writer.Writer) error {
		if err := exactReminder(engine, in.ID); err != nil {
			return err
		}
		for _, item := range engine.GetReminders(true) {
			if item.ID == in.ID {
				out.Reminder = item
				break
			}
		}
		return nil
	})
	return
}

func validateFields(title, due, priority string) error {
	if title != "" && (strings.TrimSpace(title) == "" || len(title) > 4096) {
		return invalid("title must contain text and be at most 4096 bytes")
	}
	if due != "" {
		if _, err := time.Parse("2006-01-02", due); err != nil {
			return invalid("due must be a valid YYYY-MM-DD date")
		}
	}
	if priority != "" {
		if _, ok := models.PriorityMap[priority]; !ok {
			return invalid("priority must be none, low, medium or high")
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (out MutationResult, err error) {
	if strings.TrimSpace(in.Title) == "" || in.ListID == "" {
		return out, invalid("title and list_id are required")
	}
	if err := validateFields(in.Title, in.Due, in.Priority); err != nil {
		return out, err
	}
	if in.Priority == "" {
		in.Priority = "none"
	}
	err = s.run(ctx, false, func(engine *syncengine.Engine, w *writer.Writer) error {
		if err := exactList(engine, in.ListID); err != nil {
			return err
		}
		if in.ParentID != "" {
			if err := exactReminder(engine, in.ParentID); err != nil {
				return err
			}
			parent := engine.Cache.Reminders[in.ParentID]
			if parent.ListRef == nil || *parent.ListRef != in.ListID {
				return invalid("parent_id must belong to list_id")
			}
		}
		result, err := w.AddReminder(in.Title, in.ListID, in.Due, in.Priority, in.Notes, in.ParentID)
		if err := writeError(result, err); err != nil {
			return err
		}
		id, _ := result["id"].(string)
		if id == "" {
			return &Error{"write_result_unknown", "The creation result is unknown. List reminders before retrying."}
		}
		out = MutationResult{ID: id, Status: "created"}
		return nil
	})
	return
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (out MutationResult, err error) {
	if in.ID == "" {
		return out, invalid("id is required")
	}
	if in.Title == "" && in.Due == "" && in.Priority == "" && in.Notes == "" {
		return out, invalid("provide at least one nonempty field to update")
	}
	if err := validateFields(in.Title, in.Due, in.Priority); err != nil {
		return out, err
	}
	err = s.run(ctx, false, func(engine *syncengine.Engine, w *writer.Writer) error {
		if err := exactReminder(engine, in.ID); err != nil {
			return err
		}
		result, err := w.EditReminder(in.ID, in.Title, in.Due, in.Notes, in.Priority)
		if err := writeError(result, err); err != nil {
			return err
		}
		out = MutationResult{ID: in.ID, Status: "updated"}
		return nil
	})
	return
}

func (s *Service) Complete(ctx context.Context, in IDInput) (out MutationResult, err error) {
	if in.ID == "" {
		return out, invalid("id is required")
	}
	err = s.run(ctx, false, func(engine *syncengine.Engine, w *writer.Writer) error {
		if err := exactReminder(engine, in.ID); err != nil {
			return err
		}
		if !engine.Cache.Reminders[in.ID].Completed {
			result, err := w.CompleteReminder(in.ID)
			if err := writeError(result, err); err != nil {
				return err
			}
		}
		out = MutationResult{ID: in.ID, Status: "completed"}
		return nil
	})
	return
}

func (s *Service) Delete(ctx context.Context, in DeleteInput) (out MutationResult, err error) {
	if !in.Confirm {
		return out, invalid("confirm=true is required to delete a reminder")
	}
	if in.ID == "" {
		return out, invalid("id is required")
	}
	err = s.run(ctx, false, func(engine *syncengine.Engine, w *writer.Writer) error {
		if err := exactReminder(engine, in.ID); err != nil {
			return err
		}
		result, err := w.DeleteReminder(in.ID)
		if err := writeError(result, err); err != nil {
			return err
		}
		out = MutationResult{ID: in.ID, Status: "deleted"}
		return nil
	})
	return
}

func (s *Service) Sync(ctx context.Context, in SyncInput) (out SyncResult, err error) {
	err = s.run(ctx, in.Full, func(engine *syncengine.Engine, _ *writer.Writer) error {
		out = SyncResult{Reminders: len(engine.Cache.Reminders), Lists: len(engine.Cache.Lists)}
		return nil
	})
	return
}

func exactList(engine *syncengine.Engine, id string) error {
	if _, ok := engine.Cache.Lists[id]; !ok {
		return &Error{"not_found", "List ID not found. Use list_reminder_lists to obtain an exact ID."}
	}
	return nil
}

func exactReminder(engine *syncengine.Engine, id string) error {
	if _, ok := engine.Cache.Reminders[id]; !ok {
		return &Error{"not_found", "Reminder ID not found. Use list_reminders to obtain an exact ID."}
	}
	return nil
}

func writeError(result map[string]interface{}, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return &Error{"write_result_unknown", "The write result is unknown. Inspect the reminder before retrying."}
	}
	if _, failed := result["error"]; failed {
		return &Error{"icloud_write_failed", "iCloud did not confirm the write. Inspect the reminder before retrying."}
	}
	return nil
}
