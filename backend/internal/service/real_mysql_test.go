package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/dto"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
)

// defaultRealMySQLDSN points at the user-local MariaDB instance used by the
// project's verification environment; override with GIGMATCH_TEST_MYSQL_DSN.
const defaultRealMySQLDSN = "root:@tcp(127.0.0.1:3310)/?parseTime=true&charset=utf8mb4&loc=Local"

// mysqlTestDSN is read from GIGMATCH_TEST_MYSQL_DSN. The real-persistence
// suite only runs when GIGMATCH_TEST_MYSQL=1 as well, so the default
// `go test ./...` stays green without a database.
//
// The suite runs against a real MySQL-compatible server (MySQL 8.0 or
// MariaDB 10.11+, InnoDB) over TCP with a multi-connection pool: no SQLite,
// no mocks, no single-connection serialization, no fake driver.
func mysqlSuiteDSN(t *testing.T) string {
	t.Helper()
	if os.Getenv("GIGMATCH_TEST_MYSQL") != "1" {
		t.Skip("set GIGMATCH_TEST_MYSQL=1 and GIGMATCH_TEST_MYSQL_DSN to run the real-persistence suite")
	}
	dsn := os.Getenv("GIGMATCH_TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = defaultRealMySQLDSN
	}
	return dsn
}

// mysqlSchemaDSN rewrites the DSN to point at another schema (database),
// preserving user/password/host/params. DSN shape: user:pass@tcp(host:port)/db?params
func mysqlSchemaDSN(baseDSN, schema string) string {
	at := strings.Index(baseDSN, "@")
	q := strings.Index(baseDSN, "?")
	head, params := baseDSN[:at], ""
	tail := baseDSN[at:]
	if q >= 0 {
		head, params = baseDSN[:at], baseDSN[q:]
		tail = baseDSN[at:q]
	}
	// tail looks like @tcp(host:port)/oldschema
	if slash := strings.LastIndex(tail, "/"); slash >= 0 {
		tail = tail[:slash+1] + schema
	}
	return head + tail + params
}

func mysqlAdminDB(t *testing.T, baseDSN string) *sql.DB {
	t.Helper()
	admin, err := sql.Open("mysql", mysqlSchemaDSN(baseDSN, ""))
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("ping mysql: %v", err)
	}
	return admin
}

// mysqlCounter gives each test run a unique schema name.
var mysqlCounter struct {
	sync.Mutex
	n int
}

func nextMysqlSchema() string {
	mysqlCounter.Lock()
	defer mysqlCounter.Unlock()
	mysqlCounter.n++
	return fmt.Sprintf("gigmatch_it_%d", mysqlCounter.n)
}

// realFixture is a fully wired, real-database test environment.
type realFixture struct {
	t            *testing.T
	db           *gorm.DB
	admin        *sql.DB
	schema       string
	logSvc       *OperationLogService
	notifSvc     *NotificationService
	reqSvc       *RequirementService
	bidSvc       *BidService
	contractSvc  *ContractService
	reqRepo      *repository.RequirementRepository
	bidRepo      *repository.BidRepository
	contractRepo *repository.ContractRepository
	notifRepo    *repository.NotificationRepository
	userRepo     *repository.UserRepository
	requester    *model.User
	freelancer   *model.User
}

func (f *realFixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = f.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+f.schema+"`")
	_ = f.admin.Close()
	if sqlDB, err := f.db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// newRealFixture provisions an isolated schema on the running server, runs
// the production AutoMigrate, seeds two users and wires the real services.
func newRealFixture(t *testing.T) *realFixture {
	t.Helper()
	baseDSN := mysqlSuiteDSN(t)
	admin := mysqlAdminDB(t, baseDSN)
	schema := nextMysqlSchema()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+schema+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		cancel()
		_ = admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	cancel()

	dsn := mysqlSchemaDSN(baseDSN, schema)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open gorm: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// Independent connections: concurrency exercises real InnoDB locks.
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(16)
	sqlDB.SetConnMaxLifetime(10 * time.Minute)

	if err := db.AutoMigrate(
		&model.User{}, &model.Requirement{}, &model.Bid{}, &model.Contract{},
		&model.OperationLog{}, &model.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reqRepo := repository.NewRequirementRepository(db)
	bidRepo := repository.NewBidRepository(db)
	contractRepo := repository.NewContractRepository(db)
	notifRepo := repository.NewNotificationRepository(db)
	userRepo := repository.NewUserRepository(db)

	requester := &model.User{Username: uniqueName("req"), Name: "需求方", Role: constants.RoleRequester}
	freelancer := &model.User{Username: uniqueName("free"), Name: "自由职业者", Role: constants.RoleFreelancer}
	if err := userRepo.Create(requester); err != nil {
		t.Fatalf("create requester: %v", err)
	}
	if err := userRepo.Create(freelancer); err != nil {
		t.Fatalf("create freelancer: %v", err)
	}

	logSvc := NewOperationLogService(repository.NewOperationLogRepository(db), logger)
	notifSvc := NewNotificationService(notifRepo, logger)
	f := &realFixture{
		t:            t,
		db:           db,
		admin:        admin,
		schema:       schema,
		logSvc:       logSvc,
		notifSvc:     notifSvc,
		reqSvc:       NewRequirementService(db, reqRepo, bidRepo, notifSvc, logSvc, logger),
		bidSvc:       NewBidService(db, bidRepo, reqRepo, notifSvc, logSvc, logger),
		contractSvc:  NewContractService(db, contractRepo, notifSvc, logSvc, logger),
		reqRepo:      reqRepo,
		bidRepo:      bidRepo,
		contractRepo: contractRepo,
		notifRepo:    notifRepo,
		userRepo:     userRepo,
		requester:    requester,
		freelancer:   freelancer,
	}
	t.Cleanup(f.cleanup)
	return f
}

var nameMu sync.Mutex
var nameSeq int

func uniqueName(prefix string) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	nameSeq++
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), nameSeq)
}

// --- scenario helpers --------------------------------------------------------

func (f *realFixture) publishRequirement() *model.Requirement {
	f.t.Helper()
	r, err := f.reqSvc.Create(dto.CreateRequirementRequest{
		Title:       "真实并发需求 " + uniqueName("r"),
		Description: "真实持久化环境下验证通知与业务事务一致性的需求描述",
		MinBudget:   1000, MaxBudget: 5000, Skills: []string{"Go"},
	}, f.requester.ID, f.requester.Name, f.requester.Role)
	if err != nil {
		f.t.Fatalf("[stage:publish-requirement] create: %v", err)
	}
	return r
}

func (f *realFixture) submitBid(requirementID uint, amount float64) *model.Bid {
	f.t.Helper()
	b, err := f.bidSvc.Create(dto.CreateBidRequest{
		RequirementID: requirementID, Amount: amount, DurationDays: 15,
		Proposal: "我可以按要求完成该需求开发工作并保证质量",
	}, f.freelancer.ID, f.freelancer.Name, f.freelancer.Role)
	if err != nil {
		f.t.Fatalf("[stage:submit-bid] create: %v", err)
	}
	return b
}

func (f *realFixture) accept(requirementID, bidID uint) *model.Contract {
	f.t.Helper()
	c, err := f.reqSvc.AcceptBid(requirementID, bidID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
	if err != nil {
		f.t.Fatalf("[stage:accept] accept: %v", err)
	}
	return c
}

func (f *realFixture) countNotifications(bizType string) int64 {
	f.t.Helper()
	q := f.db.Model(&model.Notification{})
	if bizType != "" {
		q = q.Where("biz_type = ?", bizType)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		f.t.Fatalf("[stage:count-notifications] %v", err)
	}
	return n
}

// breakNotifications simulates notification storage outage by moving the
// table aside (real DDL on the running server). restoreNotifications puts it
// back, modeling recovery before the client retries.
func (f *realFixture) breakNotifications(stage string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.admin.ExecContext(ctx,
		"RENAME TABLE `"+f.schema+"`.`notifications` TO `"+f.schema+"`.`notifications_broken`"); err != nil {
		f.t.Fatalf("[stage:%s] simulate notification outage: %v", stage, err)
	}
}

func (f *realFixture) restoreNotifications(stage string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.admin.ExecContext(ctx,
		"RENAME TABLE `"+f.schema+"`.`notifications_broken` TO `"+f.schema+"`.`notifications`"); err != nil {
		f.t.Fatalf("[stage:%s] restore notification table: %v", stage, err)
	}
}

func (f *realFixture) readback(stage string, fn func() error) {
	f.t.Helper()
	if err := fn(); err != nil {
		f.t.Fatalf("[stage:%s] readback assertion failed: %v", stage, err)
	}
}

// appErrorCode maps both typed AppErrors and the service-layer sentinel
// errors (constants.ErrForbidden / repository.ErrNotFound, ...) to their
// business code; -1 means an unexpected infrastructure error.
func appErrorCode(err error) int {
	var appErr *constants.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	switch {
	case errors.Is(err, constants.ErrForbidden):
		return constants.CodeForbidden
	case errors.Is(err, constants.ErrNotFound) || errors.Is(err, repository.ErrNotFound):
		return constants.CodeNotFound
	case errors.Is(err, constants.ErrConflict):
		return constants.CodeConflict
	}
	return -1
}
