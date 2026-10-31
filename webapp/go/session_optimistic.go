package isureal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

func canRunOptimistically(c *echo.Context) bool {
	if c.Request().Method != http.MethodGet {
		return false
	}
	// 通知は long poll で途中で Flush する。エクスポートは応答が大きい
	switch c.Path() {
	case "/api/notifications", "/api/me/export":
		return false
	}
	return true
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
	type result struct {
		session *Session
		err     error
	}
	done := make(chan result, 1)
	go func() {
		s, err := getSessionFromHeader(c, h.DB)
		done <- result{s, err}
	}()

	orig := c.Response()
	buf := &bufferedResponseWriter{header: http.Header{}}
	c.SetResponse(echo.NewResponse(buf, c.Logger()))
	provisional := *cached
	c.Set("session", &provisional)
	handlerErr := next(c)
	c.SetResponse(orig)

	r := <-done
	if r.err != nil {
		return false, nil, r.err, nil
	}
	if !sameSession(r.session, cached) {
		return false, r.session, nil, nil
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
