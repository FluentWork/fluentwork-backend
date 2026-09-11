package voicegateway

import (
	"context"
	"net/http"
	"net/http/httptest"
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
