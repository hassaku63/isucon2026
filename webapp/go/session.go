package isureal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type Session struct {
	Region    string    `db:"region" json:"-"`
	Token     string    `db:"-" json:"-"`
	UserID    string    `db:"user_id"`
	TokenHash string    `db:"token_hash"`
	CreatedAt time.Time `db:"created_at"`
}

func GetSession(c *echo.Context) *Session {
	sess, ok := c.Get("session").(*Session)
	if !ok {
		return nil
	}
	return sess
}

func getSessionFromHeader(c *echo.Context, db *sqlx.DB) (*Session, error) {
	authHeader := c.Request().Header.Get(echo.HeaderAuthorization)
	if authHeader == "" {
		return nil, nil
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.Logger().Warn("Invalid authorization header format(not started with Bearer)")
		return nil, NewAPIError(http.StatusUnauthorized, "invalid_session")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")

	tokenHashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(tokenHashBytes[:])
	var session Session
	err := db.GetContext(c.Request().Context(), &session, `
		SELECT s.user_id, s.token_hash, s.created_at, u.region
		FROM login_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.active = true AND u.status = 'active'
	`, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	return &session, nil
}

func SessionMiddleware(handlerContext *HandlerContext, authRequired bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			var session *Session
			validated := false
			if canRunOptimistically(c) {
				if tokenHash, ok := sessionTokenHash(c); ok {
					if v, ok := knownSessions.Load(tokenHash); ok {
						handled, checked, checkErr, handlerErr := runWithOptimisticSession(c, handlerContext, v.(*Session), next)
						if handled {
							return handlerErr
						}
						if checkErr != nil {
							return checkErr
						}
						// 覚えていた内容と違った。本体の結果は捨て、確認の結果で元どおりに処理する
						knownSessions.Delete(tokenHash)
						session, validated = checked, true
						// 確認済みなので、そのセッションで本体を最初からやり直す
						if session != nil {
							knownSessions.Store(tokenHash, session)
						}
					}
				}
			}
			if !validated {
				var err error
				session, err = getSessionFromHeader(c, handlerContext.DB)
				if err != nil {
					return err
				}
				if session != nil {
					knownSessions.Store(session.TokenHash, session)
				}
			}
			if session == nil && authRequired {
				return NewAPIError(http.StatusUnauthorized, "invalid_session")
			}
			c.Set("session", session)
			return next(c)
		}
	}
}

func InvalidateLoginSession(db *sqlx.DB, userID string, tokenHash string) error {
	// users の行のロックとセッションの無効化を 1 文（自動コミット）で行う。ロックはこの文の終わりまで持つ
	var result struct {
		Locked  int `db:"locked"`
		Updated int `db:"updated"`
	}
	if err := db.Get(&result, `
		WITH l AS (SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE),
		u AS (
			UPDATE login_sessions s
			SET active = false, invalidated_at = clock_timestamp(), reason = 'logout'
			FROM l
			WHERE s.user_id = l.id AND s.active = true AND s.token_hash = $2
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM l) AS locked, (SELECT count(*) FROM u) AS updated
	`, userID, tokenHash); err != nil {
		return fmt.Errorf("failed to invalidate sessions: %w", err)
	}
	if result.Locked == 0 {
		return fmt.Errorf("failed to lock user: %w", sql.ErrNoRows)
	}
	return nil
}

// 2026-09-03: リファクタリング後にこの関数の利用がないようです。消すのはいかがでしょうか
func CreateLoginSession(db *sqlx.DB, userID string) (*Session, error) {
	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()
	session, err := createLoginSessionTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return session, nil
}

func createLoginSessionTx(ctx context.Context, tx *sqlx.Tx, userID string) (*Session, error) {
	var lockedUserID string
	if err := tx.GetContext(ctx, &lockedUserID, `SELECT id FROM users WHERE id = $1 AND status = 'active' FOR NO KEY UPDATE`, userID); err != nil {
		return nil, fmt.Errorf("failed to lock user: %w", err)
	}
	return createLoginSessionLockedTx(ctx, tx, userID)
}

// 呼び出し側が users の行を FOR NO KEY UPDATE でロックし、status = 'active' を確かめていること。
// 既存のセッションの無効化・新しいセッションの追加・読み直しを 1 文にまとめる。
// UPDATE と INSERT は同じスナップショットで動くので、UPDATE は今回追加する行を見ない
func createLoginSessionLockedTx(ctx context.Context, tx *sqlx.Tx, userID string) (*Session, error) {
	randomToken := uuid.NewString()
	tokenHashBytes := sha256.Sum256([]byte(randomToken))
	tokenHash := hex.EncodeToString(tokenHashBytes[:])
	sessionID := uuid.NewString()
	var session Session
	if err := tx.GetContext(ctx, &session, `
  WITH inv AS (
    UPDATE login_sessions
    SET active = false, invalidated_at = clock_timestamp(), reason = 'replaced'
    WHERE user_id = $2 AND active = true
  )
  INSERT INTO login_sessions (id, user_id, token_hash)
  VALUES ($1, $2, $3)
  RETURNING user_id, token_hash, created_at
 `, sessionID, userID, tokenHash); err != nil {
		return nil, fmt.Errorf("failed to insert session: %w", err)
	}
	session.Token = randomToken
	return &session, nil
}

func getLoginSession(ctx context.Context, db sqlx.QueryerContext, sessionID string) (*Session, error) {
	var session Session
	if err := sqlx.GetContext(ctx, db, &session, `SELECT user_id, token_hash, created_at FROM login_sessions WHERE id = $1`, sessionID); err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	return &session, nil
}
