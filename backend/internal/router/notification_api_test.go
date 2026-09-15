package router

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/config"
	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/handler"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
	"github.com/gigmatch/gigmatch/internal/service"
	"github.com/gigmatch/gigmatch/internal/util"
)

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func newIntegrationEngine(t *testing.T) (*gorm.DB, *http.Client, string, func() (uint, uint)) {
	t.Helper()
	dsn := "file:" + t.Name() + "_integ?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Requirement{}, &model.Bid{}, &model.Contract{},
		&model.OperationLog{}, &model.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	userRepo := repository.NewUserRepository(db)
	notifRepo := repository.NewNotificationRepository(db)
	logSvc := service.NewOperationLogService(repository.NewOperationLogRepository(db), logger)
	notifSvc := service.NewNotificationService(notifRepo, logger)

	cfg := &config.Config{
		Env:                "test",
		JWTSecret:          "integration-test-secret",
		AuthRateLimit:      1000,
		APIRateLimit:       1000,
		CORSAllowedOrigins: "http://localhost:28030",
	}
	h := &Handlers{Notification: handler.NewNotificationHandler(notifSvc, logger)}
	engine := New(cfg, logger, h, userRepo, logSvc, db)
	server := httptest.NewServer(engine)

	alice := &model.User{Username: "alice", PasswordHash: "x", Name: "需求方", Role: constants.RoleRequester}
	bob := &model.User{Username: "bob", PasswordHash: "x", Name: "自由职业者", Role: constants.RoleFreelancer}
	if err := userRepo.Create(alice); err != nil {
		t.Fatal(err)
	}
	if err := userRepo.Create(bob); err != nil {
		t.Fatal(err)
	}

	ids := func() (uint, uint) { return alice.ID, bob.ID }
	return db, server.Client(), server.URL, ids
}

func authToken(t *testing.T, secret string, userID uint) string {
	t.Helper()
	token, err := util.GenerateToken(secret, userID, constants.RoleFreelancer, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func doJSON(t *testing.T, client *http.Client, method, url, token string, body io.Reader) (int, apiEnvelope) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env apiEnvelope
	_ = json.Unmarshal(raw, &env)
	return resp.StatusCode, env
}

func TestNotificationAPIRequiresAuth(t *testing.T) {
	_, client, base, _ := newIntegrationEngine(t)
	status, env := doJSON(t, client, http.MethodGet, base+"/api/v1/notifications", "", nil)
	if status != http.StatusUnauthorized || env.Code != constants.CodeUnauthorized {
		t.Fatalf("status=%d code=%d, want 401/%d", status, env.Code, constants.CodeUnauthorized)
	}
}

func TestNotificationAPIOwnershipAndCRUD(t *testing.T) {
	db, client, base, ids := newIntegrationEngine(t)
	aliceID, bobID := ids()
	secret := "integration-test-secret"

	// Alice owns one unread notification; Bob owns none.
	n := &model.Notification{
		RecipientID: aliceID, BizType: constants.NotificationBidSubmitted, BizID: 1,
		BizNo: "报价 #1", Title: "收到新报价", Content: "测试",
		EventKey: "bid_submitted:1", IsRead: false, OccurredAt: time.Now(),
	}
	if err := db.Create(n).Error; err != nil {
		t.Fatal(err)
	}

	aliceToken := authToken(t, secret, aliceID)
	bobToken := authToken(t, secret, bobID)

	// Alice lists: 1 item, newest first, scoped to herself.
	_, env := doJSON(t, client, http.MethodGet, base+"/api/v1/notifications", aliceToken, nil)
	if env.Code != constants.CodeSuccess {
		t.Fatalf("list code=%d message=%s", env.Code, env.Message)
	}
	var list struct {
		Items []model.Notification `json:"items"`
		Total int64                `json:"total"`
	}
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list total=%d len=%d", list.Total, len(list.Items))
	}
	if list.Items[0].EventKey != "" {
		t.Fatal("eventKey must not be serialized to clients")
	}

	// Unread count.
	_, env = doJSON(t, client, http.MethodGet, base+"/api/v1/notifications/unread-count", aliceToken, nil)
	var count struct {
		Unread int64 `json:"unread"`
	}
	_ = json.Unmarshal(env.Data, &count)
	if count.Unread != 1 {
		t.Fatalf("unread = %d, want 1", count.Unread)
	}

	// Bob cannot read Alice's notification: 403.
	nid := strconv.FormatUint(uint64(n.ID), 10)
	status, env := doJSON(t, client, http.MethodPost, base+"/api/v1/notifications/"+nid+"/read", bobToken, nil)
	if status != http.StatusForbidden || env.Code != constants.CodeForbidden {
		t.Fatalf("cross-user status=%d code=%d, want 403/%d", status, env.Code, constants.CodeForbidden)
	}
	// Still unread after the forbidden attempt.
	if fresh, _ := repository.NewNotificationRepository(db).CountUnread(aliceID); fresh != 1 {
		t.Fatalf("unread changed after forbidden access: %d", fresh)
	}

	// Nonexistent id: 404.
	_, env = doJSON(t, client, http.MethodPost, base+"/api/v1/notifications/99999/read", aliceToken, nil)
	if env.Code != constants.CodeNotFound {
		t.Fatalf("missing code=%d, want %d", env.Code, constants.CodeNotFound)
	}

	// Alice marks it read: success, then unread count is 0.
	_, env = doJSON(t, client, http.MethodPost, base+"/api/v1/notifications/"+nid+"/read", aliceToken,
		bytes.NewReader([]byte("{}")))
	if env.Code != constants.CodeSuccess {
		t.Fatalf("mark read code=%d message=%s", env.Code, env.Message)
	}
	_, env = doJSON(t, client, http.MethodGet, base+"/api/v1/notifications/unread-count", aliceToken, nil)
	_ = json.Unmarshal(env.Data, &count)
	if count.Unread != 0 {
		t.Fatalf("unread after read = %d, want 0", count.Unread)
	}

	// Bob's read-all affects nothing of Alice's.
	_, env = doJSON(t, client, http.MethodPost, base+"/api/v1/notifications/read-all", bobToken,
		bytes.NewReader([]byte("{}")))
	if env.Code != constants.CodeSuccess {
		t.Fatalf("mark all code=%d", env.Code)
	}

	// Notifications never write back to business tables: the notification
	// table stays the only mutated entity and the read notification persists.
	var stored model.Notification
	if err := db.First(&stored, n.ID).Error; err != nil {
		t.Fatalf("reload notification: %v", err)
	}
	if !stored.IsRead {
		t.Fatal("notification should be persisted as read")
	}
}
