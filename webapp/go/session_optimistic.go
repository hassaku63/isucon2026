package isureal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"
)

// トークンのハッシュ → 最後に DB で確かめたセッション。これだけで認証はしない。
// GET では、この内容を仮のセッションにして本体の処理を DB でのセッション確認と同時に走らせ、
// 確認の結果が同じときだけ本体の応答を返す（違えば捨てて元どおりに処理し直す）。
// 確認は毎回 DB でするので、ログアウト・退会・リージョンの変更の反映は元と同じ
var knownSessions sync.Map

// 応答を溜めておくための ResponseWriter
type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }
func (w *bufferedResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}
func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// 仮のセッションで先に走らせてよい書き込み。どれも書き込みは 1 つのトランザクションの中だけで、
// COMMIT の前に confirmSessionBeforeCommit で確認の結果を待つ（確認が通らなければ ROLLBACK して元どおりに処理し直す）
var optimisticWriteRoutes = map[string]bool{
	"POST /api/posts":                              true,
	"POST /api/friend-requests":                    true,
	"POST /api/friend-requests/:requestId/:action": true,
	"PUT /api/follows/:userId":                     true,
}

func canRunOptimistically(c *echo.Context) bool {
	if c.Request().Method != http.MethodGet {
		return optimisticWriteRoutes[c.Request().Method+" "+c.Path()]
	}
	// 通知は long poll で途中で Flush する
	switch c.Path() {
	case "/api/notifications":
		return false
	}
	return true
}

type sessionCheck struct {
	done    chan struct{}
	session *Session
	err     error
	cached  *Session
}

var errSessionChanged = errors.New("session changed")

// 仮のセッションで走っている書き込みは、COMMIT の前にこれを呼ぶ。DB での確認の結果が仮のセッションと違えばエラーを返す
func confirmSessionBeforeCommit(c *echo.Context) error {
	check, ok := c.Get("sessionCheck").(*sessionCheck)
	if !ok || check == nil {
		return nil
	}
	<-check.done
	if check.err != nil {
		return check.err
	}
	if !sameSession(check.session, check.cached) {
		return errSessionChanged
	}
	return nil
}

func sessionTokenHash(c *echo.Context) (string, bool) {
	authHeader := c.Request().Header.Get(echo.HeaderAuthorization)
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", false
	}
	sum := sha256.Sum256([]byte(strings.TrimPrefix(authHeader, "Bearer ")))
	return hex.EncodeToString(sum[:]), true
}

func sameSession(a, b *Session) bool {
	return a != nil && b != nil && a.UserID == b.UserID && a.TokenHash == b.TokenHash && a.Region == b.Region && a.CreatedAt.Equal(b.CreatedAt)
}

// 本体を仮のセッションで走らせる。確認の結果が同じなら応答を返して true、違えば何も書かずに false
func runWithOptimisticSession(c *echo.Context, h *HandlerContext, cached *Session, next echo.HandlerFunc) (bool, *Session, error, error) {
	isWrite := c.Request().Method != http.MethodGet
	var body []byte
	if isWrite {
		// やり直すときにもう一度読めるように、本文を先に読んでおく
		b, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return true, nil, nil, err
		}
		body = b
		c.Request().Body = io.NopCloser(bytes.NewReader(body))
	}
	check := &sessionCheck{done: make(chan struct{}), cached: cached}
	go func() {
		check.session, check.err = getSessionFromHeader(c, h.SessionDB)
		close(check.done)
	}()

	orig := c.Response()
	buf := &bufferedResponseWriter{header: http.Header{}}
	c.SetResponse(echo.NewResponse(buf, c.Logger()))
	provisional := *cached
	c.Set("session", &provisional)
	if isWrite {
		c.Set("sessionCheck", check)
	}
	handlerErr := next(c)
	c.SetResponse(orig)
	if isWrite {
		c.Set("sessionCheck", nil)
		c.Request().Body = io.NopCloser(bytes.NewReader(body))
	}

	<-check.done
	if check.err != nil {
		return false, nil, check.err, nil
	}
	if !sameSession(check.session, cached) {
		return false, check.session, nil, nil
	}
	for k, v := range buf.header {
		orig.Header()[k] = v
	}
	if buf.status != 0 {
		orig.WriteHeader(buf.status)
		if buf.body.Len() > 0 {
			if _, err := orig.Write(buf.body.Bytes()); err != nil {
				return true, nil, nil, err
			}
		}
	}
	return true, nil, nil, handlerErr
}
