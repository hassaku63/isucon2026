package isureal

import (
	"errors"
	"io"
	"net/http"

	"github.com/labstack/echo/v5"
)

type APIError struct {
	Status int
	Code   string
}

type APIErrorResponse struct {
	Error string `json:"error"`
}

func (e *APIError) Error() string { return e.Code }

func NewAPIError(status int, code string) error {
	return &APIError{Status: status, Code: code}
}

// nginx の client_max_body_size（32m）より少し大きく
const maxDrainedRequestBody = 33 << 20

func APIErrorHandler(c *echo.Context, err error) {
	if response, _ := echo.UnwrapResponse(c.Response()); response != nil && response.Committed {
		return
	}
	status, message := http.StatusInternalServerError, err.Error()
	var apiErr *APIError
	var echoErr echo.HTTPStatusCoder
	switch {
	case errors.As(err, &apiErr):
		status, message = apiErr.Status, apiErr.Code
	case errors.As(err, &echoErr):
		status = echoErr.StatusCode()
	}
	if status >= http.StatusInternalServerError {
		c.Logger().Error("request failed", "error", err)
	}
	body := APIErrorResponse{
		Error: message,
	}
	// 本文を読まずにエラーを返す経路（IP BAN の 403、セッションの 401、Content-Type の 415 など）で、
	// 残りの本文を読み捨ててから応答する。読み残しが 256KB を超えると Go が応答のあと接続を切り、
	// 本文を送っている途中の nginx には応答が届かず 502 になっていた。応答を書いたあとは読めないので、ここで読む
	_, _ = io.Copy(io.Discard, io.LimitReader(c.Request().Body, maxDrainedRequestBody))
	if writeErr := c.JSON(status, body); writeErr != nil {
		c.Logger().Error("failed to write error response", "error", writeErr)
	}
}
