package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

func TestNormalizeProjectPathPrefix(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "proj")
	insideDir := filepath.Join(projectRoot, "src")
	insideFile := filepath.Join(insideDir, "main.go")
	outside := filepath.Join(t.TempDir(), "other", "x.go")

	tests := []struct {
		name       string
		pathPrefix string
		want       string
		wantErr    bool
	}{
		{
			name:       "empty",
			pathPrefix: "",
			want:       "",
		},
		{
			name:       "relative passthrough",
			pathPrefix: "src/handlers/",
			want:       "src/handlers/",
		},
		{
			name:       "absolute inside project",
			pathPrefix: insideFile,
			want:       "src/main.go",
		},
		{
			name:       "absolute project root",
			pathPrefix: projectRoot,
			want:       "",
		},
		{
			name:       "absolute outside project",
			pathPrefix: outside,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProjectPathPrefix(tt.pathPrefix, projectRoot)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeProjectPathPrefix() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeProjectPathPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeForPathMatch_RelativePath(t *testing.T) {
	// Branche filepath.Abs : projectRoot est un chemin relatif.
	// On obtient le workdir courant pour construire le pathPrefix absolu attendu.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// projectRoot relatif pointant vers le répertoire courant
	projectRoot := "."
	// pathPrefix absolu sous le workdir courant
	pathPrefix := filepath.Join(wd, "path_normalizer.go")

	got, err := NormalizeProjectPathPrefix(pathPrefix, projectRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "path_normalizer.go" {
		t.Fatalf("got %q, want %q", got, "path_normalizer.go")
	}
}

func TestNormalizeForPathMatch_NonExistentRoot(t *testing.T) {
	// Branche EvalSymlinks error : root does not exist, fallback to unresolved path.
	// Use a sub-directory of TempDir that is never created — absolute on all platforms.
	projectRoot := filepath.Join(t.TempDir(), "nonexistent")
	pathPrefix := filepath.Join(projectRoot, "src", "foo.go")

	got, err := NormalizeProjectPathPrefix(pathPrefix, projectRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.ToSlash(filepath.Join("src", "foo.go"))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeWorkspacePathPrefix(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "projA")
	projB := filepath.Join(root, "projB")
	projNested := filepath.Join(projA, "nested")

	ws := &config.Workspace{
		Name: "ws",
		Projects: []config.ProjectEntry{
			{Name: "a", Path: projA},
			{Name: "b", Path: projB},
			{Name: "nested", Path: projNested},
		},
	}

	tests := []struct {
		name             string
		pathPrefix       string
		selectedProjects []string
		wantPrefix       string
		wantProjects     []string
		wantErr          bool
	}{
		{
			name:       "relative passthrough",
			pathPrefix: "src/",
			wantPrefix: "src/",
		},
		{
			name:         "absolute in project a",
			pathPrefix:   filepath.Join(projA, "src", "main.go"),
			wantPrefix:   "src/main.go",
			wantProjects: []string{"a"},
		},
		{
			name:         "absolute in nested project picks longest match",
			pathPrefix:   filepath.Join(projNested, "pkg", "x.go"),
			wantPrefix:   "pkg/x.go",
			wantProjects: []string{"nested"},
		},
		{
			name:             "absolute path narrowed from selected projects",
			pathPrefix:       filepath.Join(projB, "src"),
			selectedProjects: []string{"a", "b"},
			wantPrefix:       "src",
			wantProjects:     []string{"b"},
		},
		{
			name:             "absolute path not in selected projects",
			pathPrefix:       filepath.Join(projB, "src"),
			selectedProjects: []string{"a"},
			wantErr:          true,
		},
		{
			name:       "absolute path outside workspace",
			pathPrefix: filepath.Join(root, "outside", "z.go"),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPrefix, gotProjects, err := NormalizeWorkspacePathPrefix(tt.pathPrefix, ws, tt.selectedProjects)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeWorkspacePathPrefix() error = %v, wantErr %v", err, tt.wantErr)
			}
			if gotPrefix != tt.wantPrefix {
				t.Fatalf("NormalizeWorkspacePathPrefix() prefix = %q, want %q", gotPrefix, tt.wantPrefix)
			}
			if len(gotProjects) != len(tt.wantProjects) {
				t.Fatalf("NormalizeWorkspacePathPrefix() projects = %#v, want %#v", gotProjects, tt.wantProjects)
			}
			for i := range gotProjects {
				if gotProjects[i] != tt.wantProjects[i] {
					t.Fatalf("NormalizeWorkspacePathPrefix() projects = %#v, want %#v", gotProjects, tt.wantProjects)
				}
			}
		})
	}
}

func TestSplitWorkspaceScopedPath(t *testing.T) {
	ws := &config.Workspace{
		Name: "acme",
		Projects: []config.ProjectEntry{
			{Name: "backend", Path: "/srv/backend"},
			{Name: "shared", Path: "/srv/shared"},
		},
	}

	tests := []struct {
		name        string
		pathPrefix  string
		selected    []string
		wantRel     string
		wantProject string
		wantOK      bool
	}{
		{
			name:        "workspace/project/rest",
			pathPrefix:  "acme/backend/src",
			wantRel:     "src",
			wantProject: "backend",
			wantOK:      true,
		},
		{
			name:        "trailing slash",
			pathPrefix:  "acme/backend/src/",
			wantRel:     "src",
			wantProject: "backend",
			wantOK:      true,
		},
		{
			name:        "selection includes project",
			pathPrefix:  "acme/shared/lib",
			selected:    []string{"shared"},
			wantRel:     "lib",
			wantProject: "shared",
			wantOK:      true,
		},
		{
			name:       "selection excludes project",
			pathPrefix: "acme/backend/src",
			selected:   []string{"shared"},
			wantOK:     false,
		},
		{
			name:       "only two segments",
			pathPrefix: "acme/backend",
			wantOK:     false,
		},
		{
			name:       "wrong workspace name",
			pathPrefix: "other/backend/src",
			wantOK:     false,
		},
		{
			name:       "unknown project",
			pathPrefix: "acme/unknown/src",
			wantOK:     false,
		},
		{
			name:       "plain relative path",
			pathPrefix: "src/handlers",
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel, project, ok := splitWorkspaceScopedPath(tt.pathPrefix, ws, tt.selected)
			if ok != tt.wantOK {
				t.Fatalf("splitWorkspaceScopedPath() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if rel != tt.wantRel || project != tt.wantProject {
				t.Fatalf("splitWorkspaceScopedPath() = (%q, %q), want (%q, %q)", rel, project, tt.wantRel, tt.wantProject)
			}
		})
	}
}

func TestNormalizeWorkspacePathPrefix_WorkspaceScoped(t *testing.T) {
	ws := &config.Workspace{
		Name: "acme",
		Projects: []config.ProjectEntry{
			{Name: "backend", Path: "/srv/backend"},
		},
	}

	rel, projects, err := NormalizeWorkspacePathPrefix("acme/backend/src", ws, nil)
	if err != nil {
		t.Fatalf("NormalizeWorkspacePathPrefix() error = %v", err)
	}
	if rel != "src" {
		t.Fatalf("rel = %q, want %q", rel, "src")
	}
	if len(projects) != 1 || projects[0] != "backend" {
		t.Fatalf("projects = %v, want [backend]", projects)
	}

	// A prefix that is not workspace-scoped passes through unchanged.
	rel, projects, err = NormalizeWorkspacePathPrefix("src/api", ws, nil)
	if err != nil {
		t.Fatalf("NormalizeWorkspacePathPrefix() error = %v", err)
	}
	if rel != "src/api" {
		t.Fatalf("rel = %q, want %q", rel, "src/api")
	}
	if len(projects) != 0 {
		t.Fatalf("projects = %v, want unchanged empty selection", projects)
	}
}
