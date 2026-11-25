package judge

import (
	"encoding/json"

	"github.com/MIERAI/docs-platform/judgev1"
)

// toolDefs describe the judge-tools/v1 tools to a model (MCP tools/list, Ollama tools,
// tools.definitions of a judge/v1 request). Paths are always repository-relative.
var toolDefs = []judgev1.ToolDef{
	{Name: judgev1.ToolListDir, Description: "List a directory of the repository (repository-relative path, \"\" or \".\" = root).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)},
	{Name: judgev1.ToolReadFile, Description: "Read lines from_line..to_line of a file (at most 400 lines per call; lines are returned in order starting at from_line).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"from_line":{"type":"integer"},"to_line":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)},
	{Name: judgev1.ToolSearch, Description: "Search the tracked files: a case-insensitive fixed string, or with regex=true an RE2 regular expression (case-sensitive). scope is a path glob (\"*\" = everything, \"scripts/*\", \".github/workflows/*\", \"*.go\", or a directory). Returns path, line and text of each match. Absence evidence re-runs exactly this search.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"scope":{"type":"string"},"regex":{"type":"boolean"},"max_results":{"type":"integer"}},"required":["pattern"],"additionalProperties":false}`)},
	{Name: judgev1.ToolGrep, Description: "Search with an RE2 regular expression (case-sensitive) under a file or directory prefix path.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer"}},"required":["pattern"],"additionalProperties":false}`)},
	{Name: judgev1.ToolGlob, Description: "List tracked files matching a path glob (\"*\" also crosses \"/\"; \"**/*.yaml\", \"clusters/*\").",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"max_results":{"type":"integer"}},"required":["pattern"],"additionalProperties":false}`)},
	{Name: judgev1.ToolGitLog, Description: "Recent commits touching a path (\".\" = whole repository): short hash, date and subject.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"max_count":{"type":"integer"}},"additionalProperties":false}`)},
	{Name: judgev1.ToolGitShow, Description: "Show one commit (message and diff), or with path the file as it was at that commit.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"rev":{"type":"string"},"path":{"type":"string"}},"required":["rev"],"additionalProperties":false}`)},
	{Name: judgev1.ToolGitChanges, Description: "Paths deleted (D), renamed (R), added (A) or modified (M) in the history, each with the commit (hash, date, subject) that did it, newest first. kind e.g. \"DR\" (default) or \"A\".",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"},"path":{"type":"string"},"max_count":{"type":"integer"}},"additionalProperties":false}`)},
	{Name: judgev1.ToolChangesSince, Description: "What changed since a documentation line was written: the commit that last wrote it, the files it names (and named paths that no longer exist, with the commit that removed them), and later commits to those files with short diffs.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"}},"required":["path","line"],"additionalProperties":false}`)},
	{Name: judgev1.ToolFindReferences, Description: "Where an identifier, command, flag, config key, env var, file path or metric name is defined and used (definitions first), as path:line.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)},
	{Name: judgev1.ToolCheckQuote, Description: "Check that a quote occurs verbatim at path:line (±2 lines) before you use it as evidence.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer"},"text":{"type":"string"}},"required":["path","line","text"],"additionalProperties":false}`)},
}

// ToolDefs returns the tool descriptions (a copy).
func ToolDefs() []judgev1.ToolDef {
	return append([]judgev1.ToolDef{}, toolDefs...)
}
