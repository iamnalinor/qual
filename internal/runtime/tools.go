package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/report"
)

func definition(name, description, properties string, required []string, readOnly bool) domain.ToolDefinition {
	data, err := json.Marshal(map[string]any{"type": "object", "properties": json.RawMessage(properties), "required": required, "additionalProperties": false})
	if err != nil {
		panic(fmt.Sprintf("invalid built-in tool schema: %v", err))
	}
	return domain.ToolDefinition{Name: name, Description: description, InputSchema: data, ReadOnly: readOnly}
}

func (r *Runtime) definitions() []domain.ToolDefinition {
	defs := []domain.ToolDefinition{
		definition("read_file", "Read a workspace file, with optional byte range.", `{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}`, []string{"path"}, true),
		definition("write_file", "Write a workspace file.", `{"path":{"type":"string"},"content":{"type":"string"}}`, []string{"path", "content"}, false),
		definition("list_files", "List workspace paths.", `{}`, nil, true),
		definition("search", "Search workspace file contents for a literal string.", `{"query":{"type":"string"}}`, []string{"query"}, true),
		definition("shell", "Run a command in the workspace with a cleaned environment and bounded time.", `{"command":{"type":"string"},"timeout_seconds":{"type":"integer"}}`, []string{"command"}, false),
		definition("git", "Run git with an argument array in the workspace.", `{"args":{"type":"array","items":{"type":"string"}}}`, []string{"args"}, false),
		definition("ask_user", "Ask the user; noninteractive mode returns an explicit unavailable result.", `{"question":{"type":"string"}}`, []string{"question"}, false),
		definition("budget_status", "Read this role's actual and reserved budget ledger.", `{}`, nil, true),
		definition("read_artifact", "Read an immutable saved artifact by ID with an optional byte range.", `{"id":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}`, []string{"id"}, true),
		definition("finish", "Validate and preserve role outputs. Reviewer requires report/findings/manifest; leader additionally decisions; setup additionally proposals.", `{"kind":{"type":"string"},"report_path":{"type":"string"},"findings_path":{"type":"string"},"manifest_path":{"type":"string"},"decisions_path":{"type":"string"},"proposals_path":{"type":"string"}}`, []string{"kind", "report_path", "findings_path", "manifest_path"}, false),
	}
	for _, tool := range r.config.Tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

func (r *Runtime) readOnly(name string) bool {
	for _, d := range r.definitions() {
		if d.Name == name {
			return d.ReadOnly
		}
	}
	return false
}

func (r *Runtime) executeTool(ctx context.Context, state *domain.RunState, p *agentProgress, call domain.ToolCall) toolOutcome {
	outcome := toolOutcome{Call: call, Result: domain.ToolResult{CallID: call.ID}}
	if p.Phase == finalizationPhase && !finalizationCall(call) {
		outcome.Result.IsError = true
		outcome.Result.Content = "tool unavailable during budget finalization"
		return outcome
	}
	if call.Name == "shell" || call.Name == "git" {
		if p.Phase == finalizationPhase && call.Name == "git" {
			safe, err := finalizationGitCall(call)
			if err != nil {
				outcome.Result.IsError = true
				outcome.Result.Content = err.Error()
				return outcome
			}
			call = safe
		}
		outcome.Result = r.processTool(ctx, state.RunID, p.Workspace, call)
		return outcome
	}
	result, finished, err := r.dispatch(ctx, state, p, call)
	outcome.Result = result
	content := result.Content
	outcome.Finished = finished
	if err != nil {
		outcome.Result.IsError = true
		content = strings.TrimSpace(content + "\n" + err.Error())
	}
	outcome.Result.Content = content
	return r.truncateOutcome(ctx, state.RunID, outcome)
}

func (r *Runtime) truncateOutcome(ctx context.Context, id domain.RunID, outcome toolOutcome) toolOutcome {
	content := outcome.Result.Content
	if len(content) > r.config.MaxOutputBytes {
		artifact, saveErr := r.config.Store.PutArtifact(ctx, id, "tool-output", strings.NewReader(content))
		if saveErr != nil {
			outcome.Result.IsError = true
			outcome.Result.Content = saveErr.Error()
			return outcome
		}
		if outcome.Result.ArtifactID == "" {
			outcome.Result.ArtifactID = artifact.ID
		}
		outcome.Result.Truncated = true
		half := r.config.MaxOutputBytes / 2
		outcome.Result.Content = fmt.Sprintf("%s\n[TRUNCATED %d bytes; use read_artifact id=%s offset/limit]\n%s", content[:half], len(content), artifact.ID, content[len(content)-half:])
	}
	return outcome
}

func (r *Runtime) dispatchBuiltin(ctx context.Context, state *domain.RunState, p *agentProgress, call domain.ToolCall) (string, *domain.AgentResult, error) {
	switch call.Name {
	case "read_file", "write_file", "list_files", "search", "shell", "git":
		return r.workspaceTool(ctx, p, call)
	case "ask_user":
		return r.ask(ctx, call)
	case "budget_status":
		status, err := r.budgetStatus(state, p)
		if err != nil {
			return "", nil, err
		}
		data, e := json.Marshal(status)
		return string(data), nil, e
	case "read_artifact":
		s, e := r.readArtifact(ctx, state.RunID, call.Arguments)
		return s, nil, e
	case "finish":
		return r.finish(ctx, state, p, call.Arguments)
	default:
		return "", nil, coded(domain.ErrTool, "unknown tool: "+call.Name, nil)
	}
}

func (r *Runtime) ask(ctx context.Context, call domain.ToolCall) (string, *domain.AgentResult, error) {
	var a struct {
		Question string `json:"question"`
	}
	if err := report.Decode(call.Arguments, &a); err != nil {
		return "", nil, err
	}
	if r.config.Ask == nil {
		return "User input unavailable in noninteractive mode; record an explicit assumption or unresolved question.", nil, nil
	}
	answer, err := r.config.Ask(ctx, a.Question)
	return answer, nil, err
}

func confidential(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == ".env" || strings.HasPrefix(base, ".env.") || base == "credentials.json" || base == "auth.json" || base == ".netrc"
}

func confined(root, path string, write bool) (string, error) {
	if path == "" || filepath.IsAbs(path) {
		return "", coded(domain.ErrTool, "path must be relative to workspace", nil)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	clean, err := cleanPath(path)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(target)
	if write && os.IsNotExist(err) {
		resolved, err = resolveNewPath(root, target)
	}
	if err != nil {
		return "", fmt.Errorf("resolve tool path: %w", err)
	}
	if err = withinRoot(root, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func resolveNewPath(root, target string) (string, error) {
	parent := filepath.Dir(target)
	if parent == root {
		return target, nil
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if os.IsNotExist(err) {
		resolved, err = resolveNewPath(root, parent)
	}
	if err != nil {
		return "", fmt.Errorf("resolve write parent: %w", err)
	}
	return filepath.Join(resolved, filepath.Base(target)), nil
}

type fileArgs struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
}

func readFile(root string, data []byte) (string, error) {
	var a fileArgs
	if err := report.Decode(data, &a); err != nil {
		return "", err
	}
	path, err := confined(root, a.Path, false)
	if err != nil {
		return "", err
	}
	return readRange(path, a.Offset, a.Limit)
}

func readRange(path string, offset, limit int64) (content string, err error) {
	if offset < 0 || limit < 0 {
		return "", coded(domain.ErrTool, "negative byte range", nil)
	}
	if limit == 0 || limit > 4*1024*1024 {
		limit = 4 * 1024 * 1024
	}
	f, info, err := openRegular(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek file: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	content = string(data)
	if offset+int64(len(data)) < info.Size() {
		content += fmt.Sprintf("\n[RANGE ends at %d of %d bytes; read next offset]", offset+int64(len(data)), info.Size())
	}
	return content, nil
}

func writeFile(root string, data []byte) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := report.Decode(data, &a); err != nil {
		return "", err
	}
	path, err := confined(root, a.Path, true)
	if err != nil {
		return "", err
	}
	if len(a.Content) > 4*1024*1024 {
		return "", coded(domain.ErrTool, "write exceeds maximum artifact size", nil)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create file parent: %w", err)
	}
	if err = os.WriteFile(path, []byte(a.Content), 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
}

func (r *Runtime) workspaceTool(ctx context.Context, p *agentProgress, call domain.ToolCall) (string, *domain.AgentResult, error) {
	switch call.Name {
	case "read_file":
		s, e := readFile(p.Workspace, call.Arguments)
		return s, nil, e
	case "write_file":
		s, e := writeFile(p.Workspace, call.Arguments)
		return s, nil, e
	case "list_files":
		s, e := listFiles(ctx, p.Workspace, "")
		return s, nil, e
	case "search":
		var a struct {
			Query string `json:"query"`
		}
		if e := report.Decode(call.Arguments, &a); e != nil {
			return "", nil, e
		}
		s, e := listFiles(ctx, p.Workspace, a.Query)
		return s, nil, e
	case "shell", "git":
		s, e := runCommand(ctx, p.Workspace, call)
		return s, nil, e
	default:
		return "", nil, coded(domain.ErrTool, "unknown workspace tool", nil)
	}
}

func cleanPath(path string) (string, error) {
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", coded(domain.ErrTool, "path escapes workspace", nil)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if confidential(part) || part == ".git" {
			return "", coded(domain.ErrTool, "credential or git metadata path unavailable", nil)
		}
	}
	return clean, nil
}

func withinRoot(root, resolved string) error {
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return coded(domain.ErrTool, "symlink escapes workspace", err)
	}
	if _, err = cleanPath(relative); err != nil {
		return err
	}
	if confidential(resolved) {
		return coded(domain.ErrTool, "credential path unavailable", nil)
	}
	return nil
}

func openRegular(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, coded(domain.ErrTool, "only regular files can be read", nil)
	}
	// Nonblocking and no-follow also handle a final-component replacement between stat/open.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) //nolint:gosec // Caller resolved workspace confinement or immutable digest; regular file checked before and after open.
	if err != nil {
		return nil, nil, fmt.Errorf("open regular file: %w", err)
	}
	info, err = file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.Join(coded(domain.ErrTool, "file changed type while opening", nil), file.Close())
	}
	return file, info, nil
}

func (r *Runtime) dispatch(ctx context.Context, state *domain.RunState, p *agentProgress, call domain.ToolCall) (domain.ToolResult, *domain.AgentResult, error) {
	if !builtinTool(call.Name) {
		for _, tool := range r.config.Tools {
			if tool.Definition().Name == call.Name {
				result, err := tool.Execute(ctx, call)
				result.CallID = call.ID
				return result, nil, err
			}
		}
	}
	content, finished, err := r.dispatchBuiltin(ctx, state, p, call)
	return domain.ToolResult{CallID: call.ID, Content: content}, finished, err
}

func builtinTool(name string) bool {
	switch name {
	case "read_file", "write_file", "list_files", "search", "shell", "git", "ask_user", "budget_status", "read_artifact", "finish":
		return true
	default:
		return false
	}
}

func finalizationCall(call domain.ToolCall) bool {
	if !finalizationName(call.Name) {
		return false
	}
	if call.Name != "git" {
		return true
	}
	var args struct {
		Args []string `json:"args"`
	}
	if err := report.Decode(call.Arguments, &args); err != nil {
		return false
	}
	if len(args.Args) == 0 {
		return false
	}
	switch args.Args[0] {
	case "diff", "show", "log", "status":
	default:
		return false
	}
	for _, arg := range args.Args {
		if unsafeFinalGitArg(arg) {
			return false
		}
	}
	return true
}

func finalizationGitCall(call domain.ToolCall) (domain.ToolCall, error) {
	var input struct {
		Args []string `json:"args"`
	}
	if err := report.Decode(call.Arguments, &input); err != nil {
		return call, err
	}
	args := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", input.Args[0]}
	if input.Args[0] != "status" {
		args = append(args, "--no-ext-diff", "--no-textconv")
	}
	args = append(args, input.Args[1:]...)
	input.Args = args
	data, err := json.Marshal(input)
	if err != nil {
		return call, fmt.Errorf("encode finalization git args: %w", err)
	}
	call.Arguments = data
	return call, nil
}

func unsafeFinalGitArg(arg string) bool {
	return strings.Contains(arg, "ext-diff") || strings.Contains(arg, "textconv") || arg == "-c" || strings.HasPrefix(arg, "--config") || strings.HasPrefix(arg, "--output")
}
