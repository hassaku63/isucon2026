package isureal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type searchUsersRequest struct {
	Q              string `query:"q"`
	BeforeUsername string `query:"beforeUsername"`
	BeforeID       string `query:"beforeId"`
}

type searchUsersResponse struct {
	Items   []relationshipUserResponse `json:"items"`
	HasMore bool                       `json:"hasMore"`
}

func normalizeSearchText(value string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(value)))
}

// 正規化の結果は文字列だけで決まるので、ユーザー名・表示名ごとに覚えておく（検索のたびに全ユーザー分を計算していた）。
// 上限を超えたら捨てて作り直す
const normalizedSearchTextCacheLimit = 200000

var normalizedSearchTextCache = struct {
	sync.RWMutex
	m map[string]string
}{m: make(map[string]string)}

func cachedNormalizeSearchText(value string) string {
	normalizedSearchTextCache.RLock()
	normalized, ok := normalizedSearchTextCache.m[value]
	normalizedSearchTextCache.RUnlock()
	if ok {
		return normalized
	}
	normalized = normalizeSearchText(value)
	normalizedSearchTextCache.Lock()
	if len(normalizedSearchTextCache.m) >= normalizedSearchTextCacheLimit {
		normalizedSearchTextCache.m = make(map[string]string)
	}
	normalizedSearchTextCache.m[value] = normalized
	normalizedSearchTextCache.Unlock()
	return normalized
}

func searchUsersHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input searchUsersRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if !validUserText(input.Q, 1, 100) {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		if (input.BeforeUsername == "") != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		response, err := searchUsers(ctx, h.DB, viewer, input)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, response)
	}
}

func searchUsers(ctx context.Context, db *sqlx.DB, viewer string, input searchUsersRequest) (searchUsersResponse, error) {
	// 絞り込みに使う列だけを全件取り、当たった人（最大 51 人）だけ全部の列を取り直す。
	// 元は active なユーザー全員の全部の列を毎回読んでいた
	var candidates []struct {
		ID          string `db:"id"`
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
	}
	// 候補とブロックの一覧は互いに依存しないので並列に引く（往復 3 回 → 2 回）。エラーは元の順に見る
	var blocked map[string]bool
	var blockedErr error
	var wg sync.WaitGroup
	wg.Go(func() { blocked, blockedErr = getBlockedUserIDs(ctx, db, viewer) })
	candidatesErr := db.SelectContext(ctx, &candidates, `SELECT id, username, display_name FROM users WHERE status='active' AND id<>$1 AND ($2='' OR (username,id)>($2,$3)) ORDER BY username, id`, viewer, input.BeforeUsername, input.BeforeID)
	wg.Wait()
	if candidatesErr != nil {
		return searchUsersResponse{}, candidatesErr
	}
	if blockedErr != nil {
		return searchUsersResponse{}, blockedErr
	}
	query := normalizeSearchText(input.Q)
	matchedIDs := make([]string, 0, 51)
	for _, candidate := range candidates {
		if !strings.Contains(cachedNormalizeSearchText(candidate.Username), query) && !strings.Contains(cachedNormalizeSearchText(candidate.DisplayName), query) {
			continue
		}
		if blocked[candidate.ID] {
			continue
		}
		matchedIDs = append(matchedIDs, candidate.ID)
		if len(matchedIDs) == 51 {
			break
		}
	}
	var users []User
	if len(matchedIDs) > 0 {
		if err := db.SelectContext(ctx, &users, `SELECT `+strings.Join(userColumns, ", ")+` FROM users WHERE id = ANY($1) AND status='active'`, matchedIDs); err != nil {
			return searchUsersResponse{}, err
		}
	}
	usersByID := make(map[string]User, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}
	items := make([]relationshipUserResponse, 0, len(matchedIDs))
	for _, id := range matchedIDs {
		// 2 回の問い合わせの間に退会した人は除く
		user, ok := usersByID[id]
		if !ok {
			continue
		}
		if err := user.setImageKey(); err != nil {
			return searchUsersResponse{}, err
		}
		if user.AccountType != "certified_public" {
			user.Bio = ""
		}
		items = append(items, newRelationshipUserResponse(user))
	}
	hasMore := len(items) > 50
	if hasMore {
		items = items[:50]
	}
	return searchUsersResponse{Items: items, HasMore: hasMore}, nil
}

type getUserRequest struct {
	UserID string `param:"userId"`
}

type getUserResponse relationshipUserResponse

func getUserHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getUserRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		// 3 本の読み取りは互いに依存しない（user.ID は WHERE id = $1 で引いた input.UserID と同じ）ので並列に引く
		// （往復 3 回 → 1 回）。エラーと判定は元の順に見る
		var blocked bool
		var friends []string
		var blockedErr, friendsErr error
		var wg sync.WaitGroup
		wg.Go(func() { blocked, blockedErr = isBlocked(ctx, h.DB, viewer, input.UserID) })
		wg.Go(func() { friends, friendsErr = getFriendIDs(ctx, h.DB, viewer) })
		user, err := getUser(ctx, h.DB, input.UserID)
		wg.Wait()
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		if err != nil {
			return err
		}
		if blockedErr != nil {
			return blockedErr
		}
		if blocked {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		if friendsErr != nil {
			return friendsErr
		}
		if user.AccountType != "certified_public" && !slices.Contains(friends, user.ID) {
			user.Bio = ""
		}
		return c.JSON(http.StatusOK, getUserResponse(newRelationshipUserResponse(user)))
	}
}

func newRelationshipUserResponse(user User) relationshipUserResponse {
	return relationshipUserResponse{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, ProfileImageURL: newGetMeResponse(user).ProfileImageURL, Bio: user.Bio, AccountType: user.AccountType}
}
