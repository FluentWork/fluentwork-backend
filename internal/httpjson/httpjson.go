// Package httpjson writes JSON success and error responses for app-server.
package httpjson

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// RequestIDContextKey is the gin context key for the request id.
const RequestIDContextKey = "request_id"

// LoggerContextKey is the gin context key for the request logger.
const LoggerContextKey = "logger"

// OK writes a JSON 200 response.
func OK(c *gin.Context, body any) {
	c.JSON(http.StatusOK, body)
}

// Error writes the unified error envelope and aborts the request.
func Error(c *gin.Context, err error) {
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae == nil {
		ae = apierr.Internal("internal error")
	}
	if ae.HTTPStatus >= http.StatusInternalServerError {
		if value, ok := c.Get(LoggerContextKey); ok {
			if logger, ok := value.(*slog.Logger); ok && logger != nil {
				logger.Error("request failed", "err", err, "code", ae.Code)
			}
		}
	}
	status := ae.HTTPStatus
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusInternalServerError
	}
	c.AbortWithStatusJSON(status, apierr.Body{
		Code:      ae.Code,
		Message:   ae.Message,
		RequestID: RequestID(c),
	})
}

// RequestID returns the request id stored by middleware.
func RequestID(c *gin.Context) string {
	if value, ok := c.Get(RequestIDContextKey); ok {
		if id, ok := value.(string); ok {
			return id
		}
	}
	return ""
}
