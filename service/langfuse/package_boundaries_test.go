package langfuse

import (
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modulePath = "github.com/QuantumNous/new-api"

// allowedDirectImports is the design §4.1 whitelist of New API packages the
// data plane may import directly. Packages are matched by full module path:
// relay formats and errors come from relaykit/types, validated request DTOs
// from relaykit/dto; the root dto/types packages carry unrelated host-side
// concerns and are only tolerated as transitive dependencies of relay/common.
var allowedDirectImports = map[string]bool{
	modulePath + "/common":                   true,
	modulePath + "/constant":                 true,
	modulePath + "/relay/common":             true,
	modulePath + "/relay/constant":           true,
	modulePath + "/relaykit/dto":             true,
	modulePath + "/relaykit/types":           true,
	modulePath + "/setting/langfuse_setting": true,
}

func packageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "cannot locate test source file")
	return filepath.Dir(file)
}

func TestDataPlaneDirectImportsStayInsideWhitelist(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(packageDir(t), "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	fset := token.NewFileSet()
	scanned := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		scanned++
		parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			if !strings.HasPrefix(importPath, modulePath) {
				continue
			}
			assert.True(t, allowedDirectImports[importPath],
				"%s imports %s which is outside the service/langfuse whitelist", filepath.Base(file), importPath)
		}
	}
	require.NotZero(t, scanned, "expected at least one non-test source file")
}

func TestDirectImportWhitelistDistinguishesRelaykitFromRootPackages(t *testing.T) {
	assert.True(t, allowedDirectImports[modulePath+"/relaykit/dto"])
	assert.True(t, allowedDirectImports[modulePath+"/relaykit/types"])

	for _, forbidden := range []string{
		"/dto",
		"/types",
		"/service",
		"/service/langfuseconfig",
		"/model",
		"/controller",
		"/setting/config",
		"/relay/channel/openai",
	} {
		assert.False(t, allowedDirectImports[modulePath+forbidden],
			"%s must not be directly importable from service/langfuse", modulePath+forbidden)
	}
}

func listDeps(t *testing.T, pattern string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	cmd := exec.Command("go", "list", "-deps", pattern)
	cmd.Dir = filepath.Join(packageDir(t), "..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go list -deps %s failed: %s", pattern, string(out))
	return strings.Fields(string(out))
}

func TestDataPlaneDependencyClosureExcludesControlPlaneAndChannels(t *testing.T) {
	forbidden := map[string]bool{
		modulePath + "/service":                true,
		modulePath + "/model":                  true,
		modulePath + "/controller":             true,
		modulePath + "/service/langfuseconfig": true,
	}
	for _, dep := range listDeps(t, "./service/langfuse") {
		assert.False(t, forbidden[dep], "service/langfuse must not depend on %s", dep)
		assert.False(t, strings.HasPrefix(dep, modulePath+"/relay/channel/"),
			"service/langfuse must not depend on %s", dep)
	}
}

func TestRelayPackagesDoNotDependOnLangfuseControlPlane(t *testing.T) {
	for _, dep := range listDeps(t, "./relay/...") {
		assert.NotEqual(t, modulePath+"/service/langfuseconfig", dep,
			"relay packages must not depend on the Langfuse control plane")
	}
}
