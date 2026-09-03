package search

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yoanbernabeu/grepai/config"
)

// NormalizeProjectPathPrefix normalizes a search path prefix for single-project mode.
// Absolute paths are converted to project-relative slash paths.
func NormalizeProjectPathPrefix(pathPrefix, projectRoot string) (string, error) {
	if pathPrefix == "" {
		return "", nil
	}
	if !filepath.IsAbs(pathPrefix) {
		return filepath.ToSlash(pathPrefix), nil
	}
	if projectRoot == "" {
		return "", fmt.Errorf("cannot resolve absolute path %q without project root", pathPrefix)
	}

	projectRootNorm := normalizeForPathMatch(projectRoot)
	targetNorm := normalizeForPathMatch(pathPrefix)
	rel, ok, err := relativeIfContained(projectRootNorm, targetNorm)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("path %q is outside project root %q", pathPrefix, projectRoot)
	}
	return rel, nil
}

// splitWorkspaceScopedPath recognizes indexed-path prefixes of the form
// workspace/project/rest — what a caller gets by copying the file path from a
// search result — and rewrites them to the project-relative form, narrowing
// the project selection. ok is false when the prefix is not workspace-scoped
// or scopes to a project outside selectedProjects.
func splitWorkspaceScopedPath(pathPrefix string, ws *config.Workspace, selectedProjects []string) (string, string, bool) {
	segs := strings.Split(strings.Trim(pathPrefix, "/"), "/")
	if len(segs) < 3 || segs[0] != ws.Name {
		return "", "", false
	}
	for _, p := range ws.Projects {
		if p.Name != segs[1] {
			continue
		}
		if len(selectedProjects) == 0 {
			return strings.Join(segs[2:], "/"), p.Name, true
		}
		for _, sp := range selectedProjects {
			if strings.TrimSpace(sp) == p.Name {
				return strings.Join(segs[2:], "/"), p.Name, true
			}
		}
		return "", "", false
	}
	return "", "", false
}

// NormalizeWorkspacePathPrefix normalizes a search path prefix for workspace mode.
// Absolute paths are resolved to a workspace project and converted to project-relative paths.
// The returned project list may be narrowed to a single matched project for better push-down.
func NormalizeWorkspacePathPrefix(pathPrefix string, ws *config.Workspace, selectedProjects []string) (string, []string, error) {
	if pathPrefix == "" {
		return "", selectedProjects, nil
	}
	if !filepath.IsAbs(pathPrefix) {
		// A path copied from an indexed result looks like workspace/project/rest.
		// Rewrite it to the project-relative form so it filters like any other
		// relative path, narrowing to that project.
		if ws != nil {
			if rel, projectName, ok := splitWorkspaceScopedPath(pathPrefix, ws, selectedProjects); ok {
				return rel, []string{projectName}, nil
			}
		}
		return filepath.ToSlash(pathPrefix), selectedProjects, nil
	}
	if ws == nil {
		return "", nil, fmt.Errorf("cannot resolve absolute path %q without workspace configuration", pathPrefix)
	}
	if len(ws.Projects) == 0 {
		return "", nil, fmt.Errorf("workspace %q has no projects configured", ws.Name)
	}

	targetNorm := normalizeForPathMatch(pathPrefix)

	allowed := map[string]struct{}{}
	if len(selectedProjects) > 0 {
		for _, p := range selectedProjects {
			p = strings.TrimSpace(p)
			if p != "" {
				allowed[p] = struct{}{}
			}
		}
	}

	type match struct {
		projectName string
		rel         string
		rootLen     int
	}
	var best *match

	for _, p := range ws.Projects {
		rootNorm := normalizeForPathMatch(p.Path)
		rel, ok, err := relativeIfContained(rootNorm, targetNorm)
		if err != nil || !ok {
			continue
		}
		if best == nil || len(rootNorm) > best.rootLen {
			best = &match{
				projectName: p.Name,
				rel:         rel,
				rootLen:     len(rootNorm),
			}
		}
	}

	if best == nil {
		return "", nil, fmt.Errorf("path %q does not belong to any project in workspace %q", pathPrefix, ws.Name)
	}

	if len(allowed) > 0 {
		if _, ok := allowed[best.projectName]; !ok {
			return "", nil, fmt.Errorf("path %q belongs to project %q, which is not in selected projects", pathPrefix, best.projectName)
		}
		// Narrow to a single project when absolute path disambiguates project scope.
		return best.rel, []string{best.projectName}, nil
	}

	return best.rel, []string{best.projectName}, nil
}

func normalizeForPathMatch(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func relativeIfContained(root, target string) (string, bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false, fmt.Errorf("failed to compare paths %q and %q: %w", root, target, err)
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return "", true, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, nil
	}
	return filepath.ToSlash(rel), true, nil
}
