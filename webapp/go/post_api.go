package isureal

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"example.com/isureal-company/isureal/internal/jpegcheck"
)

type createPostRequest struct {
	Image        *multipart.FileHeader `form:"image"`
	Caption      string                `form:"caption"`
	Visibility   string                `form:"visibility"`
	LocationKind string                `form:"location_kind"`
	Latitude     *float64              `form:"latitude"`
	Longitude    *float64              `form:"longitude"`
	Locality     *string               `form:"locality"`
}

type postAuthorResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type createPostResponse struct {
	ID              string             `json:"id"`
	Author          postAuthorResponse `json:"author"`
	PostingWindowID string             `json:"postingWindowId"`
	ImageURL        string             `json:"imageUrl"`
	Caption         string             `json:"caption"`
	Visibility      string             `json:"visibility"`
	Location        PostLocation       `json:"location"`
	PostedAt        time.Time          `json:"postedAt"`
	OnTime          bool               `json:"onTime"`
	LateSeconds     int64              `json:"lateSeconds"`
}

func createPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Image == nil || !validUserText(input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		switch input.Visibility {
		case "friends", "friends_of_friends", "public":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		location := PostLocation{
			Kind: input.LocationKind, Latitude: input.Latitude,
			Longitude: input.Longitude, Locality: input.Locality,
		}
		if !location.valid() {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		imageFile, err := input.Image.Open()
		if err != nil {
			return err
		}
		defer imageFile.Close()
		imageData, err := io.ReadAll(imageFile)
		if err != nil {
			return err
		}
		width, height, err := jpegcheck.Validate(bytes.NewReader(imageData))
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if width > 4096 || height > 4096 {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}

		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		user, err := getUserForUpdate(ctx, tx, userID)
		if err != nil {
			return err
		}
		// 投稿期間と、その日の自分の投稿の件数を 1 往復で読む
		var window struct {
			ID        string    `db:"id"`
			LocalDate time.Time `db:"local_date"`
			StartsAt  time.Time `db:"starts_at"`
			Active    int       `db:"active"`
			Deleted   int       `db:"deleted"`
		}
		const windowQuery = `
   SELECT w.id, w.local_date, w.starts_at, c.active, c.deleted
   FROM posting_windows w
   CROSS JOIN LATERAL (
     SELECT count(*) FILTER (WHERE p.deleted_at IS NULL) AS active,
            count(*) FILTER (WHERE p.deleted_at IS NOT NULL) AS deleted
     FROM posts p JOIN posting_windows pw ON pw.id = p.posting_window_id
     WHERE p.author_id = $2 AND pw.local_date = w.local_date
   ) c
   WHERE w.region = $1 AND w.ended_at IS NULL FOR SHARE OF w
  `
		err = tx.GetContext(ctx, &window, windowQuery, user.Region, userID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.GetContext(ctx, &window, windowQuery, user.Region, userID)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NewAPIError(http.StatusConflict, "post_slot_unavailable")
			}
			return err
		}
		postedAt := time.Now().UTC()
		if window.Active != 0 || window.Deleted >= 2 || postedAt.Before(window.StartsAt) {
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		}
		// 画像と投稿の INSERT、投稿の読み直しを 1 往復で
		var post Post
		if err := tx.GetContext(ctx, &post, `
   WITH img AS (
     INSERT INTO image_objects (id, owner_user_id, data, media_type, created_at) VALUES ($4, $2, $12, 'image/jpeg', $11)
   ), p AS (
     INSERT INTO posts (id, author_id, posting_window_id, image_id, caption, visibility,
                        location_kind, latitude, longitude, locality, posted_at)
     VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
     RETURNING *
   )
   SELECT `+postColumns+`
   FROM p JOIN posting_windows w ON w.id = p.posting_window_id
  `, uuid.NewString(), userID, window.ID, uuid.NewString(), input.Caption, input.Visibility,
			location.Kind, location.Latitude, location.Longitude, location.Locality, postedAt, imageData); err != nil {
			return err
		}
		if err := confirmSessionBeforeCommit(c); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, newPostResponse(post, user))
	}
}

func newPostResponse(post Post, user User) createPostResponse {
	response := createPostResponse{
		ID: post.ID,
		Author: postAuthorResponse{
			ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
			ProfileImageURL: newGetMeResponse(user).ProfileImageURL,
			Bio:             user.Bio, AccountType: user.AccountType,
		},
		PostingWindowID: post.PostingWindowID, ImageURL: signedImageURL(user, post.ImageID),
		Caption: post.Caption, Visibility: post.Visibility, Location: post.PostLocation,
		PostedAt: post.PostedAt, OnTime: !post.PostedAt.After(post.OnTimeDeadline),
	}
	if !response.OnTime {
		response.LateSeconds = int64(post.PostedAt.Sub(post.OnTimeDeadline) / time.Second)
	}
	return response
}

type getMyCurrentPostResponse createPostResponse

func getMyCurrentPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		// 今日の投稿の ID を引く・投稿を引く・自分を引くの 3 回の問い合わせを 1 回にまとめる。
		// europe / america からは DB までの往復（約 100ms）が 3 回分かかっていた
		var post PostWithAuthor
		err := h.DB.GetContext(ctx, &post, `
   SELECT `+postColumns+`, `+userColumnsAs("u", "author")+`
   FROM posts p
   JOIN posting_windows w ON w.id = p.posting_window_id
   JOIN users u ON u.id = p.author_id
   JOIN posting_windows current ON current.region = u.region AND current.ended_at IS NULL
                              AND current.local_date = w.local_date
   WHERE p.author_id = $1 AND p.deleted_at IS NULL
  `, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		// 元の getUser は退会済みのユーザーを sql.ErrNoRows（500）にしていた
		if post.Author.Status != "active" {
			return sql.ErrNoRows
		}
		user := post.Author
		if err := user.setImageKey(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, getMyCurrentPostResponse(newPostResponse(post.Post, user)))
	}
}

type getPostRequest struct {
	PostID string `param:"postId"`
}

type getPostResponse createPostResponse

func getPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		// 投稿と閲覧者の情報は互いに依存しないので並列に引く（往復 2 回 → 1 回）。判定の順序は元のまま
		post, viewer, err, viewerErr := loadPostAndViewer(ctx, h.DB, GetSession(c).UserID, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		err = viewerErr
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err != nil || !viewer.canView(post.Post) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		return c.JSON(http.StatusOK, getPostResponse(newPostResponse(post.Post, post.Author)))
	}
}

type updatePostRequest struct {
	PostID     string  `param:"postId" json:"-"`
	Caption    *string `json:"caption"`
	Visibility *string `json:"visibility"`
}

type updatePostResponse createPostResponse

func updatePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updatePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Caption == nil && input.Visibility == nil {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Caption != nil && !validUserText(*input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Visibility != nil {
			switch *input.Visibility {
			case "friends", "friends_of_friends", "public":
			default:
				return NewAPIError(http.StatusBadRequest, "invalid_post")
			}
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// 自分の行のロックと、応答に使う users の行の読み出しを 1 文で行う（元はロックの SELECT と最後の getUser の 2 回）。
		// 元のロックは status を見ないので、ここでも見ない。status は元の getUser の位置で確かめる
		var lockedUser User
		if err := tx.GetContext(ctx, &lockedUser, `SELECT `+strings.Join(userColumns, ", ")+` FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
			return err
		}
		post, err := getPost(ctx, tx, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		if post.AuthorID != userID {
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		// getPost は posting_windows を JOIN して ended_at を読んでいるので、getPostingWindow で読み直さない
		if post.WindowEndedAt != nil {
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		// 指定された項目の更新と読み直しを 1 文で行う。指定されていない項目は NULL を渡して元の値のまま
		if err := tx.GetContext(ctx, &post, `
			WITH p AS (
				UPDATE posts SET caption = COALESCE($2::text, caption), visibility = COALESCE($3::text, visibility)
				WHERE id = $1
				RETURNING *
			)
			SELECT `+postColumns+`
			FROM p JOIN posting_windows w ON w.id = p.posting_window_id
			WHERE p.deleted_at IS NULL`, post.ID, input.Caption, input.Visibility); err != nil {
			return err
		}
		if lockedUser.Status != "active" {
			return sql.ErrNoRows
		}
		user := lockedUser
		if err := user.setImageKey(); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, updatePostResponse(newPostResponse(post, user)))
	}
}

type deletePostRequest struct {
	PostID string `param:"postId" json:"-"`
}

func deletePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deletePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var lockedUserID string
		if err := tx.GetContext(ctx, &lockedUserID, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
			return err
		}
		// 投稿と、その日（posting_windows.local_date）に自分が消した投稿の数を 1 文で読む。
		// 数えるのは自分（userID）の投稿なので、作者が自分でないときは下で 403 を返して使わない
		var postWithDeletions struct {
			Post
			Deletions int `db:"deletions"`
		}
		err = tx.GetContext(ctx, &postWithDeletions, `
   SELECT `+postColumns+`,
     (SELECT count(*) FROM posts p2 JOIN posting_windows w2 ON w2.id = p2.posting_window_id
      WHERE p2.author_id = $2 AND w2.local_date = w.local_date AND p2.deleted_at IS NOT NULL) AS deletions
   FROM posts p JOIN posting_windows w ON w.id = p.posting_window_id
   WHERE p.id = $1 AND p.deleted_at IS NULL
  `, input.PostID, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		post := postWithDeletions.Post
		if post.AuthorID != userID {
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		deletions := postWithDeletions.Deletions
		if deletions >= 2 {
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE posts SET deleted_at = clock_timestamp() WHERE id = $1`, post.ID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
