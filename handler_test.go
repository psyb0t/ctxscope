package ctxscope

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHandler returns a logger writing JSON through a Handler, plus a reader
// for the single record it emitted. Records are decoded rather than string-
// matched so a nested attribute cannot pass as a top-level one.
func newTestHandler(t *testing.T) (*slog.Logger, func() map[string]any) {
	t.Helper()

	buf := &bytes.Buffer{}
	logger := slog.New(NewHandler(slog.NewJSONHandler(buf, nil)))

	return logger, func() map[string]any {
		t.Helper()

		record := map[string]any{}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &record))

		return record
	}
}

func TestHandler_AppliesContextScope(t *testing.T) {
	t.Parallel()

	logger, read := newTestHandler(t)

	ctx := Set(context.Background(), Attr("request_id", "abc"))
	logger.InfoContext(ctx, "hello")

	assert.Equal(t, "abc", read()["request_id"])
}

// A plain call loses the per-context tier but NOT the global one, which never
// came from a context to begin with. Asserting both halves — an earlier version
// of this test only checked the absence and let the docs overclaim.
func TestHandler_PlainCallKeepsGlobalTierOnly(t *testing.T) {
	// NOT parallel: SetGlobal is process-wide state.
	SetGlobal(Attr("service", "svc"))
	t.Cleanup(func() {
		RemoveGlobal("service")
	})

	logger, read := newTestHandler(t)

	logger.Info("hello")

	record := read()
	assert.NotContains(t, record, "request_id")
	assert.Equal(t, "svc", record["service"])
}

// The README tells people to install the handler with slog.SetDefault and then
// use the package-level slog calls. That is a different path from driving a
// logger built here, and nothing covered it.
func TestHandler_PackageLevelSlogAfterSetDefault(t *testing.T) {
	// NOT parallel: swaps the process-wide default logger.
	buf := &bytes.Buffer{}

	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	slog.SetDefault(slog.New(NewHandler(slog.NewJSONHandler(buf, nil))))

	ctx := Set(context.Background(), Attr("request_id", "abc"))
	slog.InfoContext(ctx, "hello")

	record := map[string]any{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "abc", record["request_id"])
}

func TestHandler_EmptyScopeAddsNothing(t *testing.T) {
	t.Parallel()

	logger, read := newTestHandler(t)

	logger.InfoContext(context.Background(), "hello")

	record := read()
	assert.Equal(t, "hello", record["msg"])
	assert.Len(t, record, 3) // time, level, msg — and nothing else
}

func TestHandler_ScopeStaysAtTopLevelUnderGroup(t *testing.T) {
	t.Parallel()

	logger, read := newTestHandler(t)

	ctx := Set(context.Background(), Attr("request_id", "abc"))
	logger.WithGroup("payload").InfoContext(ctx, "hello", "size", 3)

	record := read()

	// The whole reason Handler replays ops instead of adding attrs to the
	// record: request_id must NOT end up inside the group.
	assert.Equal(t, "abc", record["request_id"])

	payload, ok := record["payload"].(map[string]any)
	require.True(t, ok, "group should be an object")
	assert.NotContains(t, payload, "request_id")
	assert.InDelta(t, float64(3), payload["size"], 0)
}

func TestHandler_WithAttrsStillApply(t *testing.T) {
	t.Parallel()

	logger, read := newTestHandler(t)

	ctx := Set(context.Background(), Attr("request_id", "abc"))
	logger.With("component", "api").InfoContext(ctx, "hello")

	record := read()
	assert.Equal(t, "abc", record["request_id"])
	assert.Equal(t, "api", record["component"])
}

// Mutation check: the merge has to happen per record. A Handler that resolved
// the scope once at construction passes every other test in this file and fails
// this one.
func TestHandler_SeesScopeSetAfterLoggerBuilt(t *testing.T) {
	t.Parallel()

	logger, read := newTestHandler(t)

	derived := logger.With("component", "api")
	ctx := Set(context.Background(), Attr("request_id", "late"))

	derived.InfoContext(ctx, "hello")

	assert.Equal(t, "late", read()["request_id"])
}

// Two handlers derived from one parent must not write over each other's ops
// through a shared backing array — the reason withOp clips before appending.
func TestHandler_DerivedHandlersDoNotShareOps(t *testing.T) {
	t.Parallel()

	bufA, bufB := &bytes.Buffer{}, &bytes.Buffer{}

	parent := NewHandler(slog.NewJSONHandler(bufA, nil))
	first := slog.New(parent.WithAttrs([]slog.Attr{slog.String("branch", "first")}))

	parentB := NewHandler(slog.NewJSONHandler(bufB, nil))
	second := slog.New(parentB.WithAttrs([]slog.Attr{slog.String("branch", "second")}))

	first.InfoContext(context.Background(), "a")
	second.InfoContext(context.Background(), "b")

	recordA, recordB := map[string]any{}, map[string]any{}
	require.NoError(t, json.Unmarshal(bufA.Bytes(), &recordA))
	require.NoError(t, json.Unmarshal(bufB.Bytes(), &recordB))

	assert.Equal(t, "first", recordA["branch"])
	assert.Equal(t, "second", recordB["branch"])
}

func TestHandler_WithAttrsAndWithGroupAreNoOpsWhenEmpty(t *testing.T) {
	t.Parallel()

	handler := NewHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	assert.Same(t, handler, handler.WithAttrs(nil))
	assert.Same(t, handler, handler.WithGroup(""))
}

func TestHandler_EnabledDelegates(t *testing.T) {
	t.Parallel()

	opts := &slog.HandlerOptions{Level: slog.LevelWarn} //nolint:exhaustruct // only Level matters here
	handler := NewHandler(slog.NewJSONHandler(&bytes.Buffer{}, opts))

	ctx := context.Background()
	assert.False(t, handler.Enabled(ctx, slog.LevelInfo))
	assert.True(t, handler.Enabled(ctx, slog.LevelError))
}

// failingHandler reports a failure from Handle so the wrap path is reachable.
// WithAttrs and WithGroup return the receiver rather than promoting to an
// embedded handler — otherwise applying the scope would hand back a plain
// handler and the failure would vanish before Handle is ever called.
type failingHandler struct {
	err error
}

func (failingHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h failingHandler) Handle(_ context.Context, _ slog.Record) error {
	return h.err
}

func (h failingHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

func (h failingHandler) WithGroup(_ string) slog.Handler {
	return h
}

func TestHandler_WrapsInnerError(t *testing.T) {
	t.Parallel()

	sentinel := ctxerrors.New("inner exploded")
	inner := failingHandler{err: sentinel}

	ctx := Set(context.Background(), Attr("request_id", "abc"))

	// slog discards the handler's error on a normal logging call, so drive
	// Handle directly — otherwise the wrap is unreachable and untested.
	err := NewHandler(inner).
		Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0))

	require.ErrorIs(t, err, sentinel)
}

func TestHandler_GlobalAndContextTiersMerge(t *testing.T) {
	// NOT parallel: SetGlobal is process-wide state.
	SetGlobal(Attr("service", "svc"), Attr("commit", "deadbeef"))
	t.Cleanup(func() {
		RemoveGlobal("service", "commit")
	})

	logger, read := newTestHandler(t)

	// The context tier must win the collision on "commit".
	ctx := Set(context.Background(),
		Attr("request_id", "abc"),
		Attr("commit", "from-context"),
	)
	logger.InfoContext(ctx, "hello")

	record := read()
	assert.Equal(t, "svc", record["service"])
	assert.Equal(t, "abc", record["request_id"])
	assert.Equal(t, "from-context", record["commit"])
}
