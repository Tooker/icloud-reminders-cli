// Package mcpserver exposes a standalone Reminders MCP server.
package mcpserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"icloud-reminders/internal/reminders"
)

func New(backend reminders.Backend, version string, logger *slog.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "icloud-reminders", Version: version}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{},
		Instructions: "Use list_reminder_lists and list_reminders to obtain exact IDs. Writes change iCloud data; obtain the user's approval before invoking them. Deletion requires confirm=true. Never blindly retry a failed creation or an uncertain write. Authentication is an administrative CLI operation; run reminders auth when auth_required is returned.",
	})
	read := annotations(true, false, true)
	create := annotations(false, false, false)
	update := annotations(false, true, true)
	complete := annotations(false, false, true)
	remove := annotations(false, true, false)
	register(server, "list_reminder_lists", "List iCloud Reminders lists and their exact IDs.", read, logger,
		func(ctx context.Context, _ struct{}) (reminders.ListsResult, error) { return backend.ListLists(ctx) })
	register(server, "list_reminders", "List reminders with optional list, parent, title, completion and pagination filters. Due dates use YYYY-MM-DD.", read, logger, backend.List)
	register(server, "get_reminder", "Read one reminder by its exact ID, including notes and parent/list references.", read, logger, backend.Get)
	register(server, "create_reminder", "Create a reminder in an existing list, optionally as a subtask. Do not automatically retry failed or timed-out creations.", create, logger, backend.Create)
	register(server, "update_reminder", "Update specified nonempty fields of a reminder. Set priority=none to clear priority; clearing notes or due dates is unsupported.", update, logger, backend.Update)
	register(server, "complete_reminder", "Mark a reminder complete. Already completed reminders are left unchanged.", complete, logger, backend.Complete)
	register(server, "delete_reminder", "Permanently delete a reminder. Requires explicit confirm=true.", remove, logger, backend.Delete)
	register(server, "sync_reminders", "Refresh the local Reminders cache. Full sync can take several minutes; this tool does not mutate iCloud.", read, logger, backend.Sync)
	return server
}

func annotations(read, destructive, idempotent bool) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: &closed}
}

func register[In, Out any](server *mcp.Server, name, description string, annotations *mcp.ToolAnnotations, logger *slog.Logger, operation func(context.Context, In) (Out, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			started := time.Now()
			callID := uuid.NewString()
			if logger != nil {
				logger.Info("mcp_tool_start", "tool", name, "call_id", callID)
			}
			out, err := operation(ctx, in)
			outcome := "ok"
			if err != nil {
				err = reminders.PublicError(err)
				outcome = err.(*reminders.Error).Code
			}
			if logger != nil {
				fields := []any{"tool", name, "call_id", callID, "outcome", outcome, "duration_ms", time.Since(started).Milliseconds()}
				if err == nil {
					fields = append(fields, "result_count", resultCount(out))
				}
				logger.Info("mcp_tool_complete", fields...)
			}
			return nil, out, err
		})
}

func resultCount(out any) int {
	switch value := out.(type) {
	case reminders.ListsResult:
		return len(value.Lists)
	case reminders.Page:
		return len(value.Reminders)
	case reminders.SyncResult:
		return value.Reminders
	default:
		return 1
	}
}
