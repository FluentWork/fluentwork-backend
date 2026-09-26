package httpserver

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// moduleRoot is the repository root as seen from this package's directory.
const moduleRoot = "../.."

// emitterDeclaration matches the exposition function every emitter exports.
var emitterDeclaration = regexp.MustCompile(`func \w*PrometheusMetrics\(\) string`)

// emittersOnDisk returns the module-relative directories declaring an emitter.
func emittersOnDisk(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != moduleRoot && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !emitterDeclaration.Match(body) {
			return nil
		}
		rel, relErr := filepath.Rel(moduleRoot, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", moduleRoot, err)
	}
	slices.Sort(found)
	return slices.Compact(found)
}

func registeredPackages() []string {
	registered := make([]string, 0, len(metricsRegistry))
	for _, emitter := range metricsRegistry {
		registered = append(registered, emitter.pkg)
	}
	slices.Sort(registered)
	return registered
}

func TestEveryMetricsEmitterInTheModuleIsRegistered(t *testing.T) {
	onDisk := emittersOnDisk(t)
	// Anti-vacuity: without a floor, a broken walk would make this test pass by
	// finding nothing.
	if len(onDisk) < 7 {
		t.Fatalf("source scan found %d emitters (%v); it must see at least the seven that exist today", len(onDisk), onDisk)
	}
	registered := registeredPackages()
	if !slices.Equal(onDisk, registered) {
		t.Fatalf("emitters on disk = %v, registered in metricsRegistry = %v", onDisk, registered)
	}
}

func TestNoMetricFamilyIsRenderedByTwoEmitters(t *testing.T) {
	owner := make(map[string]string)
	for _, emitter := range metricsRegistry {
		families := familiesIn(emitter.render())
		if len(families) == 0 {
			t.Fatalf("%s renders no metric family at all", emitter.pkg)
		}
		for _, family := range families {
			if previous, taken := owner[family]; taken && previous != emitter.pkg {
				t.Fatalf("metric family %q is rendered by both %s and %s; a scraper would reject the payload", family, previous, emitter.pkg)
			}
			owner[family] = emitter.pkg
		}
	}
}

func TestMetricsServesEveryRegisteredEmitter(t *testing.T) {
	store := account.NewMemoryStore()
	cfg := config.Config{HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret}
	server := New(cfg, nil, account.NewHandler(account.NewService(store, account.NopReassigner{}, cfg, nil)),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, store.Ping)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, emitter := range metricsRegistry {
		for _, family := range familiesIn(emitter.render()) {
			if !strings.Contains(body, family) {
				t.Fatalf("GET /metrics does not serve %s from %s", family, emitter.pkg)
			}
		}
	}
}
