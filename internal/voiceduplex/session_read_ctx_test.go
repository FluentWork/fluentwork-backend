package voiceduplex

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 读侧与写侧是同一个形状：**窗口到期 = 整条 duplex 死亡**。
//
// coder/websocket 把「读用的 ctx 被取消」变成「整个 Conn 被关掉」——
// read.go:232 的 setupReadTimeout 与写侧的 setupWriteTimeout 是同一个
// `context.AfterFunc(ctx, c.close)`。而 collectTurn 那条读用的正是带轮窗口
// deadline 的 ctx（volc_duplex.go 的 readCtx）。于是：
//
//	一轮静默到超时 → outcome=timeout（一个**设计好的正常状态**）
//	                 → 但 socket 已经被库关掉了
//	下一轮          → ErrDuplexClosed，失败被算到别的原因头上
//
// 日志里那一行长得完全无害。这是这条判据存在的理由：一次普通的窗口过期，
// 不该有任何连接级后果。
//
// 写成结构断言，是因为这个后果**确定**（不像写侧那次是概率），但也正因为
// 「窗口」这件事会不断有人新加，规则必须钉在「socket 的读由谁喂 ctx」上，
// 而不是钉在某一次测试的时序上。
func TestEveryFrameOffTheWireUsesTheSessionReadContext(t *testing.T) {
	t.Parallel()

	label := func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.Ident:
			return v.Name
		case *ast.SelectorExpr:
			if x, ok := v.X.(*ast.Ident); ok {
				return x.Name + "." + v.Sel.Name
			}
			return v.Sel.Name
		}
		return "?"
	}

	type site struct {
		fn   string
		file string
		line int
		arg  string
	}

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var rawReads []site // 直接把 .Read( 打在某个连接上的调用点
	var recvs []site    // recv(...) 的调用点
	var cancels []site  // 取消会话级读 ctx 的点

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if ok {
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						if fun.Name == "recv" && len(call.Args) > 0 {
							recvs = append(recvs, site{fn.Name.Name, name, fset.Position(call.Pos()).Line, label(call.Args[0])})
						}
					case *ast.SelectorExpr:
						switch fun.Sel.Name {
						case "recv":
							if len(call.Args) > 0 {
								recvs = append(recvs, site{fn.Name.Name, name, fset.Position(call.Pos()).Line, label(call.Args[0])})
							}
						case "Read":
							rawReads = append(rawReads, site{fn.Name.Name, name, fset.Position(call.Pos()).Line, ""})
						case "cancelRead":
							cancels = append(cancels, site{fn.Name.Name, name, fset.Position(call.Pos()).Line, ""})
						}
					}
				}
				return true
			})
		}
	}

	// 反空洞：提取分支坏掉时必须报「形状失效」，不能报「仓库干净」。
	if len(recvs) == 0 {
		t.Fatal("一个 recv 调用点都没扫到 —— 提取分支失效，本判据给不出结论。")
	}

	// socket 的读只能有一条路。多一条，下面那条 ctx 规则就只管住了一半。
	if len(rawReads) != 1 {
		t.Fatalf("生产代码里直接调 .Read( 的地方 = %+v，只允许**一处**（唯一 reader）。", rawReads)
	}
	if rawReads[0].fn != "readLoop" {
		t.Errorf("socket 的读出现在 %s（%s:%d），只允许在 readLoop 里 —— "+
			"任何别的函数里读，就意味着有一个地方拿着自己的 ctx 在决定这条连接的死活。",
			rawReads[0].fn, rawReads[0].file, rawReads[0].line)
	}

	// 会话级读 ctx 必须有释放点，且只该在 Close 里释放：
	// 它不该在轮边界、update 窗口或 drain 窗口被取消 —— 那正是这条票的病因。
	if len(cancels) == 0 {
		t.Fatal("找不到任何 cancelRead —— 会话级读 ctx 没有释放点，或者字段被改名了，本判据给不出结论。")
	}
	for _, c := range cancels {
		if c.fn != "Close" {
			t.Errorf("%s:%d（%s 内）取消了会话级读 ctx —— 它只该在 Close 里被取消。"+
				"在一个窗口上取消它，等于让那次窗口过期把整条 duplex 带走。", c.file, c.line, c.fn)
		}
	}
}

// 一轮超时**不是**会话的死刑。
//
// TurnOutcomeTimeout 是一个设计好的正常状态（provider 静默整个窗口），会照常
// 报给 iOS。它若把 socket 一起带走，下一轮的失败就会长成 `ErrDuplexClosed`，
// 被归到「网络断了」那类原因上 —— 而真正的原因是上一轮的一次静默。
//
// 这里第 1 轮必然超时（服务端在窗口内一声不响），第 2 轮必须成功。
// 复现是确定的：修之前，第 1 轮超时那一刻 socket 就被库关掉了。
func TestATurnTimeoutDoesNotKillTheSession(t *testing.T) {
	t.Parallel()

	// 服务端：握手后先沉默足够久（盖过第 1 轮的窗口），再补上一整轮事件。
	// 写出用忽略错误的写法 —— 修之前这条连接会被客户端关掉，服务端写失败是
	// **症状**，不该让服务端先于客户端把测试打红，那样就看不清是谁的错。
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-read-ctx"}}`)
		time.Sleep(600 * time.Millisecond)
		write := func(payload string) {
			_ = conn.Write(context.Background(), websocket.MessageText, []byte(payload))
		}
		write(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		write(`{"type":"response.output_text.delta","delta":"Hi."}`)
		write(`{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	// 第 1 轮：服务端在整个窗口里一声不响 —— 必然超时。
	first, firstErr := session.collectTurn(context.Background(), time.Now(), nil, 250*time.Millisecond)
	if first.Outcome != TurnOutcomeTimeout {
		t.Fatalf("第 1 轮 outcome = %q（err=%v），want timeout —— 判据的前提没成立。",
			first.Outcome, firstErr)
	}

	// 第 2 轮：服务端会正常回一轮，会话必须还在。
	second, secondErr := session.collectTurn(context.Background(), time.Now(), nil, 3*time.Second)
	if secondErr != nil {
		t.Fatalf("一轮超时之后，第 2 轮就失败了：%v —— 一次普通的窗口过期把会话带走了。", secondErr)
	}
	if second.Outcome != TurnOutcomeOK {
		t.Fatalf("第 2 轮 outcome = %q，want ok", second.Outcome)
	}
}

// 读侧同样：会话必须比「打开它的那个 ctx」活得久。
//
// 与写侧那条对称（TestTheSessionOutlivesTheContextThatOpenedIt）。生产上
// defaultResetDuplex 带着 `resetCtx, cancel := WithTimeout(ctx, …)` 开会话，
// 然后 `defer cancel()` —— 函数一返回那个 ctx 就没了。readLoop 的 ctx 若从
// 调用者的 ctx 派生，它会在那一刻被打死，而这一次是整个会话：唯一那条读路径
// 没了，之后每一次 recv 都只会报死连接。
func TestTheReaderOutlivesTheContextThatOpenedIt(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-reader-ctx"}}`)
		// 发得晚一点，确保事件到达时「打开会话的那个 ctx」早已撤销。
		time.Sleep(80 * time.Millisecond)
		_ = conn.Write(context.Background(), websocket.MessageText,
			[]byte(`{"type":"response.output_text.delta","delta":"still here"}`))
		<-make(chan struct{})
	})

	openCtx, cancel := context.WithCancel(context.Background())
	session, err := OpenDuplex(openCtx, DuplexConfig{
		APIKey:   "test-key-not-used-by-mock",
		Endpoint: url,
		Model:    "test-model",
		Voice:    "test-voice",
	})
	if err != nil {
		t.Fatalf("OpenDuplex: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	cancel() // 打开它的那个调用已经返回
	time.Sleep(20 * time.Millisecond)

	ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	evt, err := session.recv(ctx)
	if err != nil {
		t.Fatalf("打开会话的 ctx 撤销之后，会话读不出来了：%v", err)
	}
	if evt.Delta != "still here" {
		t.Fatalf("读到 %+v，want delta=still here", evt)
	}
}

// 窗口过期之后，socket 必须还能读。
//
// 这是上一条的最小形式，直接打在 recv 上：先让一次带期限的读过期，然后
// 服务端发一条事件 —— 它必须读得到。修之前那次过期已经把连接关了，
// 第二次读拿到的是 `use of closed network connection` 而不是那条事件。
func TestAWindowExpiryLeavesTheSocketReadable(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-window"}}`)
		time.Sleep(300 * time.Millisecond)
		_ = conn.Write(context.Background(), websocket.MessageText,
			[]byte(`{"type":"response.output_text.delta","delta":"late"}`))
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	shortCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err := session.recv(shortCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("窗口过期的读返回 %v，want context.DeadlineExceeded —— 判据的前提没成立。", err)
	}

	longCtx, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	evt, err2 := session.recv(longCtx)
	cancel2()
	if err2 != nil {
		t.Fatalf("窗口过期之后 socket 再也读不出来了：%v —— 一次普通的窗口过期把连接带走了。", err2)
	}
	if evt.Delta != "late" {
		t.Fatalf("读到的事件是 %+v，want delta=late", evt)
	}
}
