package isureal

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type createFriendRequestRequest struct {
	RecipientID string `json:"recipientId"`
}

type createFriendRequestResponse struct {
	RelationID string    `json:"relationId"`
	Status     string    `json:"status"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func createFriendRequestHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createFriendRequestRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		tx, err := beginRelationshipTx(ctx, h.DB, actor, input.RecipientID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// ブロック・友達かどうか・保留中の申請を 1 往復で調べる（申請は 1 件目を使う。元の Get と同じ）
		var state struct {
			Blocked            bool           `db:"blocked"`
			Friends            bool           `db:"friends"`
			PendingID          sql.NullString `db:"pending_id"`
			PendingRequesterID sql.NullString `db:"pending_requester_id"`
		}
		if err := tx.GetContext(ctx, &state, `
   SELECT EXISTS(SELECT 1 FROM block_relations WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1)) AS blocked,
          EXISTS(SELECT 1 FROM friendships WHERE (user_low_id=$1 AND user_high_id=$2) OR (user_low_id=$2 AND user_high_id=$1)) AS friends,
          pending.id AS pending_id, pending.requester_id AS pending_requester_id
   FROM (SELECT 1) AS one
   LEFT JOIN LATERAL (
     SELECT id, requester_id FROM friend_requests WHERE status='pending' AND
      ((requester_id=$1 AND recipient_id=$2) OR (requester_id=$2 AND recipient_id=$1))
     LIMIT 1
   ) pending ON true
  `, actor, input.RecipientID); err != nil {
			return err
		}
		if state.Blocked || state.Friends {
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		}
		var request FriendRequest
		if state.PendingID.Valid {
			if state.PendingRequesterID.String == actor {
				return NewAPIError(http.StatusConflict, "relationship_conflict")
			}
			// 相手からの申請を承認し、友達にする。書き込みと読み直しを 1 往復で
			pending := FriendRequest{ID: state.PendingID.String, RequesterID: state.PendingRequesterID.String, RecipientID: actor}
			if err := tx.GetContext(ctx, &request, `
   WITH f AS (INSERT INTO friendships (id, user_low_id, user_high_id, source_request_id) VALUES ($2,$3,$4,$1))
   UPDATE friend_requests SET status='accepted', updated_at=clock_timestamp() WHERE id=$1
   RETURNING id, requester_id, recipient_id, status, created_at, updated_at
  `, pending.ID, uuid.NewString(), min(pending.RequesterID, pending.RecipientID), max(pending.RequesterID, pending.RecipientID)); err != nil {
				return err
			}
		} else {
			if err := tx.GetContext(ctx, &request, `
   INSERT INTO friend_requests (id, requester_id, recipient_id, status, created_at, updated_at) VALUES ($1,$2,$3,'pending',clock_timestamp(),clock_timestamp())
   RETURNING id, requester_id, recipient_id, status, created_at, updated_at
  `, uuid.NewString(), actor, input.RecipientID); err != nil {
				return err
			}
		}
		if err := confirmSessionBeforeCommit(c); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, createFriendRequestResponse{RelationID: request.ID, Status: request.Status, UpdatedAt: request.UpdatedAt})
	}
}

type transitionFriendRequestRequest struct {
	RequestID string `param:"requestId" json:"-"`
	Action    string `param:"action" json:"-"`
}

type transitionFriendRequestResponse createFriendRequestResponse

func transitionFriendRequestHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input transitionFriendRequestRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		var status string
		switch input.Action {
		case "accept":
			status = "accepted"
		case "reject":
			status = "rejected"
		case "cancel":
			status = "cancelled"
		default:
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		request, err := getFriendRequest(ctx, h.DB, input.RequestID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		if err != nil {
			return err
		}
		if (status == "cancelled" && actor != request.RequesterID) || (status != "cancelled" && actor != request.RecipientID) {
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		}
		tx, err := beginRelationshipTx(ctx, h.DB, request.RequesterID, request.RecipientID)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// 申請の読み直しとブロックの確認を 1 往復で
		var locked struct {
			FriendRequest
			Blocked bool `db:"blocked"`
		}
		if err := tx.GetContext(ctx, &locked, `
   SELECT r.id, r.requester_id, r.recipient_id, r.status, r.created_at, r.updated_at,
          EXISTS(SELECT 1 FROM block_relations WHERE (blocker_id=r.requester_id AND blocked_id=r.recipient_id) OR (blocker_id=r.recipient_id AND blocked_id=r.requester_id)) AS blocked
   FROM friend_requests r WHERE r.id=$1
  `, input.RequestID); err != nil {
			return err
		}
		request = locked.FriendRequest
		if request.Status != "pending" {
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		}
		// 状態の更新と読み直し（承認なら友達の追加も）を 1 往復で
		const updateRequest = `
   UPDATE friend_requests SET status=$2, updated_at=clock_timestamp() WHERE id=$1
   RETURNING id, requester_id, recipient_id, status, created_at, updated_at
  `
		if status == "accepted" {
			if locked.Blocked {
				return NewAPIError(http.StatusConflict, "relationship_conflict")
			}
			err = tx.GetContext(ctx, &request, `
   WITH f AS (INSERT INTO friendships (id, user_low_id, user_high_id, source_request_id) VALUES ($3,$4,$5,$1))`+updateRequest,
				request.ID, status, uuid.NewString(), min(request.RequesterID, request.RecipientID), max(request.RequesterID, request.RecipientID))
		} else {
			err = tx.GetContext(ctx, &request, updateRequest, request.ID, status)
		}
		if err != nil {
			return err
		}
		if err := confirmSessionBeforeCommit(c); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, transitionFriendRequestResponse{RelationID: request.ID, Status: request.Status, UpdatedAt: request.UpdatedAt})
	}
}
