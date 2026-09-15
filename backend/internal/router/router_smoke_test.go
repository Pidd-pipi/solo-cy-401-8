package router

import (
	"io"
	"log/slog"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/config"
)

func TestNewRegistersNotificationRoutes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:router_smoke?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Env:                "test",
		JWTSecret:          "test-secret",
		AuthRateLimit:      100,
		APIRateLimit:       100,
		CORSAllowedOrigins: "http://localhost:28030",
	}

	// Handler method values are only invoked per-request, so nil handlers
	// are sufficient for route registration. Registration must not panic on
	// the mixed static/param routes (/notifications/unread-count vs
	// /notifications/:id/read, ...).
	engine := New(cfg, logger, &Handlers{}, nil, nil, db)

	want := map[string]bool{
		"GET-/api/v1/notifications":              false,
		"GET-/api/v1/notifications/unread-count": false,
		"POST-/api/v1/notifications/read-all":    false,
		"POST-/api/v1/notifications/:id/read":    false,
	}
	for _, ri := range engine.Routes() {
		key := ri.Method + "-" + ri.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("route %s not registered", key)
		}
	}
}
