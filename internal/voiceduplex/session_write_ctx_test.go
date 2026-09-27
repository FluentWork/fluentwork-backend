package voiceduplex

import (
	"context"
	"encoding/json"
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

// 一张帧写到线上时，用的是**会话自己的** ctx，绝不是调用者的。
//
// coder/websocket 把「写用的 ctx 被取消」直接变成「整条连接被关掉」：
// Conn.Write 在写期间注册 context.AfterFunc(ctx, c.close)
// （conn.go:171 的 setupWriteTimeout，由 write.go:276-277 每次写时挂上、写完摘掉）。
// 对库来说「调用者撤销了」和「这条 socket 死了」是同一件事 —— 于是一个撤销的
// 调用者会连同**之后每一轮**一起带走。
//
// 而这条路在每一轮边界上都会被走到：collectTurn 起静音泵，泵每 20ms 写一帧，
// 轮结束时 `defer stopSilence()` 取消泵的 ctx。取消发生在哪一帧的写窗口里，
// 那条 socket 就在哪一刻死。下一轮于是报 ErrDuplexClosed，而调用者那边
// 没有任何东西能解释它。
//
// 这条判据写成结构断言，是因为**竞争没法被任何一次时序测试承诺**：实测在
// 不带 -race 的干净树上单实例跑 30 个进程，这个机会只有约十分之三。
// 规则本身很窄：写帧的 ctx 只能来自会话。
func TestEveryFrameOnTheWireUsesTheSessionWriteContext(t *testing.T) {
	t.Parallel()

	// 打印表达式用的极简标签，够本判据点名即可。
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

	var writes []site    // writeJSON(...) 的调用点
	var rawWrites []site // 直接把 .Write( 打在某个连接上的调用点

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
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "writeJSON" && len(call.Args) > 0 {
						writes = append(writes, site{fn.Name.Name, name, fset.Position(call.Pos()).Line, label(call.Args[0])})
					}
				case *ast.SelectorExpr:
					if fun.Sel.Name == "Write" {
						rawWrites = append(rawWrites, site{fn.Name.Name, name, fset.Position(call.Pos()).Line, ""})
					}
				}
				return true
			})
		}
	}

	// 反空洞：提取分支坏掉时必须报「形状失效」，不能报「仓库干净」。
	if len(writes) == 0 {
		t.Fatal("一个 writeJSON 调用点都没扫到 —— 提取分支失效，本判据给不出结论。")
	}

	// 写帧的路只能有一条。多一条就管不住它，下面那条 ctx 规则也就只管住了一半。
	if len(rawWrites) != 1 || rawWrites[0].fn != "writeJSON" {
		t.Fatalf("生产代码里直接调 .Write( 的地方 = %+v，只允许 writeJSON 内部那一处。", rawWrites)
	}

	const sessionCtx = "s.writeCtx"
	for _, w := range writes {
		if w.fn == "Close" {
			// 唯一例外，且理由具体：Close 写的是**已经被它自己脱开的**那条连接，
			// 而且它本身就是拆除动作 —— 调用者在告别帧上撤销，只该让告别帧发不出去，
			// 不该把已经决定要关的连接再关一次。
			continue
		}
		if w.arg != sessionCtx {
			t.Errorf("%s:%d（%s 内）writeJSON 的 ctx 是 %q，必须是会话级 %s。"+
				"传调用者的 ctx 意味着调用者一撤销，库就把整条 duplex 关掉。",
				w.file, w.line, w.fn, w.arg, sessionCtx)
		}
	}

	// 会话级 ctx 必须有释放点，而且释放点只能是 Close：
	// 它不该在轮边界、请求边界或任何别的地方被取消。
	var cancels []site
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
				sel, ok := n.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "cancelWrite" {
					cancels = append(cancels, site{fn.Name.Name, name, fset.Position(sel.Pos()).Line, ""})
				}
				return true
			})
		}
	}
	if len(cancels) == 0 {
		t.Fatal("找不到任何 s.cancelWrite —— 会话级写 ctx 没有释放点，或者字段被改名了，本判据给不出结论。")
	}
	for _, c := range cancels {
		if c.fn != "Close" {
			t.Errorf("%s:%d（%s 内）取消了会话级写 ctx —— 它只该在 Close 里被取消。", c.file, c.line, c.fn)
		}
	}
}

// startRecordingDuplexServer 完成握手后一直读客户端帧，把每一帧的 type 报出来。
//
// 它不会主动停：连接死掉正是下面那些测试要找的东西，而读错误就是服务端得知的方式。
func startRecordingDuplexServer(t *testing.T) (string, <-chan string) {
	t.Helper()
	seen := make(chan string, 128)
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		defer close(seen)
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-write-ctx"}}`)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, data, err := conn.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			var env struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			select {
			case seen <- env.Type:
			default:
			}
		}
	})
	return url, seen
}

func waitForClientFrame(t *testing.T, seen <-chan string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got, ok := <-seen:
			if !ok {
				t.Fatalf("服务端的读循环退出了（连接被关），始终没等到 %q。", want)
			}
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("等了 %s 也没收到客户端帧 %q。", timeout, want)
		}
	}
}

// 调用者的撤销不是会话的死刑。
//
// 这里灌进去的是一个**已经撤销**的调用者 ctx，重复若干次：它是真实那条路
// （写窗口里被取消）最容易触发的一端，而且完全确定 —— 不需要去撞时序。
//
// 重复是因为那条路径上还有一个 ctx-aware 的锁（conn.go:276 的 mu.lock），它在
// ctx 已 Done 时会和「拿到锁」随机二选一，所以单独一次灌入只有大约一半机会
// 真的走到注册 AfterFunc 那一步。
func TestACallerCancellationDoesNotKillTheSocket(t *testing.T) {
	t.Parallel()

	url, seen := startRecordingDuplexServer(t)
	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	withdrawn, cancel := context.WithCancel(context.Background())
	cancel()

	// 这一帧发不发得出去、报不报错，都不是这条判据要管的：写窗口里那个
	// AfterFunc 和写本身谁先跑完，本来就没有承诺。判据只钉后果 —— 连接还在不在。
	for i := 0; i < 6; i++ {
		_ = session.CommitAudio(withdrawn)
	}

	// 把「若被注册了，库那个 AfterFunc 会什么时候跑」的时间让出来。
	time.Sleep(50 * time.Millisecond)

	if err := session.CommitAudio(context.Background()); err != nil {
		t.Fatalf("一次调用者撤销之后，会话再也写不出帧了：%v —— 整条 duplex 已经被库关掉。", err)
	}
	waitForClientFrame(t, seen, "input_audio_buffer.commit", 2*time.Second)
}

// 会话必须比「打开它的那个 ctx」活得久。
//
// 这不是假想：生产上 defaultResetDuplex 用
// `resetCtx, cancel := context.WithTimeout(ctx, duplexResetTimeout)` 开新会话，
// 然后 `defer cancel()` —— 函数一返回，那个 ctx 就没了，而会话还要接着服务后面的每一轮。
// 会话级写 ctx 若直接从它派生，重连之后的每一次写都会打在一条已经作废的 ctx 上，
// 而在这个库上那就等于打在一条已经关掉的 socket 上。
func TestTheSessionOutlivesTheContextThatOpenedIt(t *testing.T) {
	t.Parallel()

	url, seen := startRecordingDuplexServer(t)
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

	for i := 0; i < 6; i++ {
		_ = session.CommitAudio(context.Background())
	}
	time.Sleep(20 * time.Millisecond)

	if err := session.CommitAudio(context.Background()); err != nil {
		t.Fatalf("打开会话的 ctx 撤销之后，会话再也写不出帧了：%v", err)
	}
	waitForClientFrame(t, seen, "input_audio_buffer.commit", 2*time.Second)
}

// 静音泵挂在轮边界上是**每一轮**都会发生的事，不是偶发。
//
// 一轮结束时 collectTurn 取消泵的 ctx；泵每 20ms 写一帧，取消落在哪一帧的写窗口里，
// 那条 socket 就在哪一刻被库关掉 —— 下一轮直接 ErrDuplexClosed。轮数越多，
// 这件事越躲不掉，所以这里连跑八轮：用户那边看到的是「说着说着，后面每一轮都断」。
func TestConsecutiveTurnsSurviveTheSilencePump(t *testing.T) {
	t.Parallel()

	const turns = 6
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-pump-turns"}}`)
		for i := 0; i < turns; i++ {
			writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
			writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Hi."}`)
			// 80ms 不是随手挑的：泵每 20ms 写一帧，停 4 个整节拍意味着
			// 轮结束的那一刻正好压在泵的一次写起手上 —— 这就是线上那个
			// 已知 flake 的形状（同文件里那个 08-12 的用例就是 80ms）。
			// 停 25ms 这种错开半个节拍的停法，取消永远落不进写的窗口，
			// 修之前也一次都不会红：测不出来，也就没有牙。
			time.Sleep(80 * time.Millisecond)
			writeJSONFrame(t, conn, `{"type":"response.done"}`)
		}
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	for i := 0; i < turns; i++ {
		turn, err := session.collectTurn(context.Background(), time.Now(), nil, 5*time.Second)
		if err != nil {
			t.Fatalf("第 %d 轮：%v", i+1, err)
		}
		if turn.Outcome != TurnOutcomeOK {
			t.Fatalf("第 %d 轮 outcome = %q，want ok", i+1, turn.Outcome)
		}
	}
}
