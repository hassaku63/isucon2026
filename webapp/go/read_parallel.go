package isureal

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/jmoiron/sqlx"
)

// 投稿と閲覧者の情報を並列に引く（往復 2 回 → 1 回）。
// 並列に問い合わせるので、トランザクション（*sqlx.Tx）では使えない。DB のプール（*sqlx.DB）専用
func loadPostAndViewer(ctx context.Context, db *sqlx.DB, viewerID, postID string) (PostWithAuthor, viewerContext, error, error) {
	var viewer viewerContext
	var viewerErr error
	var wg sync.WaitGroup
	wg.Go(func() { viewer, viewerErr = loadViewerContext(ctx, db, viewerID) })
	post, postErr := getPostWithAuthor(ctx, db, postID)
	wg.Wait()
	return post, viewer, postErr, viewerErr
}

// canViewPost と同じ判定。投稿と閲覧者の情報を並列に引く。読み取り専用のハンドラから DB のプールで使う
func canViewPostDB(ctx context.Context, db *sqlx.DB, viewerID, postID string) (bool, error) {
	post, viewer, err, viewerErr := loadPostAndViewer(ctx, db, viewerID, postID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if errors.Is(viewerErr, sql.ErrNoRows) {
		return false, nil
	}
	if viewerErr != nil {
		return false, viewerErr
	}
	return viewer.canView(post.Post), nil
}

// canComment と同じ判定（判定を変えるときは canComment と揃える）。投稿と閲覧者の情報を並列に引く。
// 読み取り専用のハンドラから DB のプールで使う
func canCommentDB(ctx context.Context, db *sqlx.DB, viewerID, postID string) (bool, error) {
	post, viewer, err, viewerErr := loadPostAndViewer(ctx, db, viewerID, postID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if errors.Is(viewerErr, sql.ErrNoRows) {
		return false, nil
	}
	if viewerErr != nil {
		return false, viewerErr
	}
	if !viewer.canView(post.Post) || post.WindowEndedAt != nil {
		return false, nil
	}
	if viewer.ID == post.AuthorID {
		return viewer.HasPostedToday, nil
	}
	if post.Visibility == "public" {
		return true, nil
	}
	return viewer.Friends[post.AuthorID], nil
}
