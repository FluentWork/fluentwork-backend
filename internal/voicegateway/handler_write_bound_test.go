package voicegateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// testWriteTimeout is short so this test costs ~1s instead of the production
// defaultWriteTimeout. stalledWriteBudget is the assertion bound and must stay
// comfortably above it.
const (
	testWriteTimeout   = 750 * time.Millisecond
	stalledWriteBudget = 5 * time.Second
)

// 2026-09-12: 写路径曾经没有任何上界 —— 一个不再读的手机能把整条会话拖住。
//
// 三件事叠出那条路径：
//
//   - `sendJSON` / `sendOutbound` 共用一把 `writeMu`
//   - `conn.Write` 用的是 session ctx —— 没有 deadline
//   - sink 是同步调用，跑在 collect goroutine 上（volc_duplex.go:64-66
//     要求它"must not block"，但当时没有任何东西保证）
//
// 后果不止"这次写没发出去"：collect goroutine 停在 sink 里就不再 `recv`，
// 上游 duplex 的发送缓冲随之堆积；读循环下一次要写时也要卡在 `writeMu` 上
// —— `interrupt` 分支尤其致命，它无条件调用 `sendOutbound`，即使 provider
// 返回的是 `nil, nil`（只置位、不打日志以外的事），也会去抢那把锁。于是
// `interruptedThisTurn` 永远置不上，长 TTS 打断重新变成空操作。
//
// 不变量：**一次写必须有界。** 这条测试只钉这一条，不规定上界取多少 ——
// 注意实现用的 ctx deadline 在 `coder/websocket` 里会 `close()` **整条连接**，
// 见 `defaultWriteTimeout` 对"为什么断连才是对的"的说明。
func TestSessionRuntime_WriteToStalledClientIsBounded(t *testing.T) {
	t.Parallel()

	// 服务端接受后**从不读** —— 这就是"手机不收了"。
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		accepted <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()

	client, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.CloseNow() }()

	var serverConn *websocket.Conn
	select {
	case serverConn = <-accepted:
	case <-dialCtx.Done():
		t.Fatal("server never accepted the connection")
	}

	// 客户端**不读**。写到远超任何 socket 缓冲的量，写一定阻塞在这里。
	payload := make([]byte, 16<<20)
	rt := &sessionRuntime{writeTimeout: testWriteTimeout}

	// 关键：**测试不给 `sendOutbound` 传 deadline**。上界必须来自它自己 ——
	// 否则这条测试只是在验证 `conn.Write` 会尊重调用方设的 ctx，等于什么都没验。
	done := make(chan error, 1)
	go func() {
		done <- rt.sendOutbound(context.Background(), serverConn, []ProviderOutbound{{Binary: payload}})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write to a stalled client reported success")
		}
		t.Logf("写有界地返回了：%v", err)
	case <-time.After(stalledWriteBudget):
		t.Fatalf(
			"write to a stalled client did not return within %s —— 写路径没有上界。\n"+
				"collect goroutine 会一直停在 sink 里不再 recv（上游缓冲堆积），\n"+
				"读循环下一次写也会卡在同一把 writeMu 上。",
			stalledWriteBudget,
		)
	}
}

// The guard above configures its own timeout, so it would still pass if the
// zero-value fallback regressed. This pins the other half: a sessionRuntime
// built directly — as tests and any future caller may do — must resolve to a
// real bound rather than to zero (which would mean "no deadline").
func TestSessionRuntime_ZeroWriteTimeoutResolvesToDefault(t *testing.T) {
	t.Parallel()

	if got := (&sessionRuntime{}).resolveWriteTimeout(); got != defaultWriteTimeout {
		t.Fatalf("resolveWriteTimeout() for a zero value = %s, want %s", got, defaultWriteTimeout)
	}
	if got := (&sessionRuntime{writeTimeout: testWriteTimeout}).resolveWriteTimeout(); got != testWriteTimeout {
		t.Fatalf("resolveWriteTimeout() ignored an explicit value: got %s", got)
	}
}

// 2026-09-12: `sendOutbound` 先取锁、再遍历 —— 即使没有任何字节会离开进程。
//
// `writeProviderOutbound` 只写 `Control` 和 `Binary`；只带 `ServerASRText` 的
// outbound（B14 徽章载体）什么都不写。但锁已经在这里被抢过了。
//
// 生产上最要命的是 `interrupt` 分支：它**无条件**调用 `sendOutbound`，而
// provider 对 interrupt 返回的是 `nil, nil`。于是读循环为了发"零个字节"，
// 去等一把被阻塞写持有的锁 —— `interruptedThisTurn` 因此永远置不上，
// 长 TTS 打断退回空操作（`docs/67` §2.1）。
//
// 不变量：**不产生任何字节的调用，不得参与写锁的竞争。**
func TestSessionRuntime_OutboundThatWritesNothingDoesNotContendForTheWriteLock(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		outbound []ProviderOutbound
	}{
		{"nil", nil},
		{"empty slice", []ProviderOutbound{}},
		{"only ServerASRText (B14 badge carrier)", []ProviderOutbound{{ServerASRText: "hello"}}},
		{"empty control and empty binary", []ProviderOutbound{{Control: nil, Binary: nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &sessionRuntime{}

			// Hold the lock for the whole subtest: this models a write that is
			// in flight (and, before WriteTimeout existed, blocked forever).
			rt.writeMu.Lock()
			defer rt.writeMu.Unlock()

			// A nil conn is deliberate: nothing may reach the wire here, so
			// nothing may dereference it either.
			done := make(chan error, 1)
			go func() {
				done <- rt.sendOutbound(context.Background(), nil, tc.outbound)
			}()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("sendOutbound with nothing to write returned %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal(
					"sendOutbound took writeMu even though nothing would reach the wire —— " +
						"读循环会在这里被一次阻塞的写挡住（interrupt 就是这条路径）。",
				)
			}
		})
	}
}

// The guard above would also pass if `sendOutbound` simply stopped taking the
// lock at all. This pins the other side: a payload that *does* reach the wire
// must still serialize behind an in-flight write.
func TestSessionRuntime_WritableOutboundStillWaitsForTheWriteLock(t *testing.T) {
	t.Parallel()

	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		accepted <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.CloseNow() }()

	var serverConn *websocket.Conn
	select {
	case serverConn = <-accepted:
	case <-ctx.Done():
		t.Fatal("server never accepted the connection")
	}

	// Drain, so the post-unlock write can actually complete rather than run
	// into its own deadline.
	go func() {
		for {
			if _, _, err := client.Read(ctx); err != nil {
				return
			}
		}
	}()

	rt := &sessionRuntime{writeTimeout: testWriteTimeout}
	rt.writeMu.Lock()

	done := make(chan error, 1)
	go func() {
		done <- rt.sendOutbound(ctx, serverConn, []ProviderOutbound{{Binary: []byte("x")}})
	}()

	select {
	case <-done:
		rt.writeMu.Unlock()
		t.Fatal("a writable outbound did not wait for writeMu —— 锁不再串行化了")
	case <-time.After(300 * time.Millisecond):
		// Expected: still queued behind the in-flight write.
	}
	rt.writeMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write after the lock was released: %v", err)
		}
	case <-time.After(stalledWriteBudget):
		t.Fatal("sendOutbound never proceeded after the lock was released")
	}
}

// `writableOutbound` 与 `writeProviderOutbound` 各自枚举一遍"哪些字段会到
// 线上"。**两处分离是有意的，不是遗漏**：
//
//	漏更新谓词 → 多抢一次锁（无害，最坏是等一个 WriteTimeout）
//	漏更新写侧 → 字段静默不上线（丢数据）
//
// 所以合并成单一谓词反而把"无害的漏"换成"有害的漏"。真正该挡的是"有人
// 加了字段却两处都没想起来" —— 这条用反射把它变成一个**必须做的决定**。
func TestProviderOutbound_EveryFieldIsClassified(t *testing.T) {
	t.Parallel()

	// true  = 会写到线上（writableOutbound 与 writeProviderOutbound 都要认）
	// false = 不上线，注释说明它承载什么
	classified := map[string]bool{
		"Control":       true,
		"Binary":        true,
		"ServerASRText": false, // B14 徽章载体：服务端内部用，不上线
	}

	typ := reflect.TypeOf(ProviderOutbound{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if _, ok := classified[name]; !ok {
			t.Fatalf(
				"ProviderOutbound 新增了字段 %q，尚未分类。\n"+
					"  会写到线上 → 同时更新 writableOutbound 与 writeProviderOutbound，并在此标 true\n"+
					"  不上线     → 在此标 false，并写明它承载什么",
				name,
			)
		}
	}
	if len(classified) != typ.NumField() {
		t.Fatalf(
			"classified 有 %d 项，ProviderOutbound 有 %d 个字段 —— 有字段已删除（清理此表），或有一项从未存在",
			len(classified), typ.NumField(),
		)
	}
}
