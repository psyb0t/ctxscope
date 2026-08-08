package ctxscope

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The global tier is process-wide state, so these tests do NOT call
// t.Parallel() — they would otherwise leak attributes into every other test's
// log output. Go runs the sequential tests in this package to completion before
// the parallel ones resume, and each of these clears up after itself.
func setGlobalForTest(t *testing.T, key string, value string) {
	t.Helper()

	SetGlobal(Attr(key, value))
	t.Cleanup(func() {
		RemoveGlobal(key)
	})
}

func TestSetGlobal_AppliesToEveryLogLine(t *testing.T) {
	setGlobalForTest(t, "commit", "deadbeef")

	assert.Equal(t, Scope{"commit": "deadbeef"}, GetGlobal())

	buf := captureDefault(t)

	GetLogger(context.Background()).Info(testLogMessage)

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "deadbeef", record["commit"])
}

func TestSetGlobal_ContextTierWinsOnCollision(t *testing.T) {
	setGlobalForTest(t, "env", "from-global")

	buf := captureDefault(t)

	ctx := Set(context.Background(), Attr("env", "from-context"))

	GetLogger(ctx).Info(testLogMessage)

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "from-context", record["env"])
}

// The load-bearing guarantee. A process fact must never cross a hop, or the
// receiving service's own value gets overwritten by the sender's and its logs
// name the wrong deploy.
func TestSetGlobal_NeverSerializedByToJSON(t *testing.T) {
	setGlobalForTest(t, "commit", "deadbeef")

	ctx := Set(context.Background(), Attr("request_id", "abc"))

	data, err := ToJSON(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"request_id":"abc"}`, string(data))

	// ...and reading it back on the far side leaves that side's global alone.
	got, err := FromJSON(context.Background(), data)
	require.NoError(t, err)

	assert.Equal(t, Scope{"request_id": "abc"}, Get(got))
	assert.Equal(t, Scope{"commit": "deadbeef"}, GetGlobal())
}

func TestGetGlobal_ReturnsACopy(t *testing.T) {
	setGlobalForTest(t, "service", "api")

	got := GetGlobal()
	got["service"] = "mutated"
	got["injected"] = "nope"

	assert.Equal(t, Scope{"service": "api"}, GetGlobal())
}

func TestRemoveGlobal(t *testing.T) {
	SetGlobal(Attr("temporary", "x"))
	require.Equal(t, Scope{"temporary": "x"}, GetGlobal())

	RemoveGlobal("temporary")
	assert.Equal(t, Scope{}, GetGlobal())

	// Removing what was never set is a no-op, not a panic.
	RemoveGlobal("never-set")
	assert.Equal(t, Scope{}, GetGlobal())
}

// GetLogger reads the global tier on every call, so concurrent writes against
// concurrent reads is the normal case, not an exotic one. Run under -race.
func TestSetGlobal_ConcurrentWritesAndReads(t *testing.T) {
	t.Cleanup(func() {
		for _, key := range []string{"a", "b", "c", "d"} {
			RemoveGlobal(key)
		}
	})

	const goroutines = 8

	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Go(func() {
			SetGlobal(Attr("a", i))
			SetGlobal(Attr("b", "x"))
			_ = GetGlobal()
			GetLogger(context.Background())
			RemoveGlobal("b")
			SetGlobal(Attr("c", true))
		})
	}

	wg.Wait()

	// Every writer set "c"; whoever won, the map is coherent and "a" survived.
	got := GetGlobal()
	assert.Equal(t, true, got["c"])
	assert.Contains(t, got, "a")
}
