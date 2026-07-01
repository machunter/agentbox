// Package mcpfs is a small filesystem MCP server, jailed to a single root
// directory. agentbox launches it as a subprocess of itself (via the "mcp-fs"
// subcommand) and connects to it with ADK's mcptoolset, giving the agent
// structured, read-only file access alongside the broader run_bash tool.
//
// It is intentionally read-only: run_bash already covers mutation, while these
// tools give the model cleaner primitives for inspecting files. The container
// remains the real trust boundary; the root jail here is defense in depth.
package mcpfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxReadBytes caps a single read_file response so the model isn't flooded.
const maxReadBytes = 256 * 1024

// maxSearchResults caps how many matches search_files returns.
const maxSearchResults = 200

// Serve runs the filesystem MCP server over stdio, jailed to root, until the
// context is cancelled or stdin closes. It must write nothing to stdout except
// the MCP protocol; diagnostics go to stderr.
func Serve(ctx context.Context, root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	abs = filepath.Clean(abs)
	// Resolve the root's own symlinks up front so the prefix check in
	// safeResolve compares real paths (e.g. /tmp -> /private/tmp on macOS).
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-fs", Version: "0.1.0"}, nil)
	registerTools(srv, abs)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// --- tool inputs ---

type pathInput struct {
	Path string `json:"path" jsonschema:"path relative to the workspace root; empty or '.' means the root itself"`
}

type searchInput struct {
	Query string `json:"query" jsonschema:"case-insensitive substring to match against file names"`
	Path  string `json:"path" jsonschema:"directory to search under, relative to the workspace root; empty means the root"`
}

func registerTools(srv *mcp.Server, root string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_directory",
		Description: "List the entries of a directory within the workspace. Returns each entry's name, type, and size.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pathInput) (*mcp.CallToolResult, any, error) {
		out, err := listDir(root, in.Path)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file within the workspace and return its contents (truncated if very large).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pathInput) (*mcp.CallToolResult, any, error) {
		out, err := readFile(root, in.Path)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_files",
		Description: "Recursively find files whose name contains the query (case-insensitive), within the workspace.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		out, err := searchFiles(root, in.Path, in.Query)
		return textResult(out, err), nil, nil
	})
}

// textResult wraps output (or an error) as an MCP tool result. Errors are
// returned in-band (IsError) so the model can read and adapt.
func textResult(text string, err error) *mcp.CallToolResult {
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// --- pure helpers (independently testable) ---

// safeResolve maps a caller-supplied path to an absolute path guaranteed to lie
// within root, rejecting traversal escapes. root must already be absolute+clean.
func safeResolve(root, rel string) (string, error) {
	if rel == "" || rel == "." {
		return root, nil
	}
	clean := filepath.Clean(rel)
	clean = strings.TrimPrefix(clean, string(filepath.Separator)) // treat absolute input as root-relative
	full := filepath.Join(root, clean)
	if !withinRoot(root, full) {
		return "", fmt.Errorf("path %q escapes the workspace root", rel)
	}
	// Lexical cleaning alone can't stop a symlink inside the jail from pointing
	// outside it (run_bash shares this container and can create one). Resolve
	// symlinks on both root and target and re-verify the real destination is
	// still within the real root.
	realRoot := root
	if rr, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = rr
	}
	real, err := resolveReal(full)
	if err != nil {
		return "", err
	}
	if !withinRoot(realRoot, real) {
		return "", fmt.Errorf("path %q escapes the workspace root", rel)
	}
	return full, nil
}

// withinRoot reports whether p is root itself or lies beneath it.
func withinRoot(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// resolveReal returns p with symlinks resolved, tolerating a not-yet-existing
// leaf: it resolves the longest existing ancestor (which is what an attacker
// could have pointed elsewhere) and rejoins the remaining components. This lets
// safeResolve guard paths that don't exist yet without falsely rejecting them.
func resolveReal(p string) (string, error) {
	cur := p
	var tail []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil // nothing along the path exists; lexical form stands
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func listDir(root, rel string) (string, error) {
	dir, err := safeResolve(root, rel)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "(empty directory)", nil
	}
	var b strings.Builder
	for _, e := range entries {
		info, ierr := e.Info()
		switch {
		case e.IsDir():
			fmt.Fprintf(&b, "%s/\n", e.Name())
		case ierr != nil:
			fmt.Fprintf(&b, "%s\t(?)\n", e.Name())
		default:
			fmt.Fprintf(&b, "%s\t%d bytes\n", e.Name(), info.Size())
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func readFile(root, rel string) (string, error) {
	path, err := safeResolve(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory, not a file", rel)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > maxReadBytes {
		return string(data[:maxReadBytes]) + fmt.Sprintf("\n\n[truncated at %d bytes of %d]", maxReadBytes, len(data)), nil
	}
	return string(data), nil
}

func searchFiles(root, rel, query string) (string, error) {
	base, err := safeResolve(root, rel)
	if err != nil {
		return "", err
	}
	if query == "" {
		return "", fmt.Errorf("query is empty")
	}
	needle := strings.ToLower(query)

	var matches []string
	truncated := false
	err = filepath.WalkDir(base, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), needle) {
			relPath, rerr := filepath.Rel(root, p)
			if rerr != nil {
				relPath = p
			}
			matches = append(matches, relPath)
			if len(matches) >= maxSearchResults {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no files matching %q", query), nil
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n\n[truncated at %d results]", maxSearchResults)
	}
	return out, nil
}
