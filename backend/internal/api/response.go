package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/GuardHer/AegisVision/backend/internal/domain"
)

// Envelope is the unified REST API response wrapper.
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func respondOK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{
		Code:    domain.CodeOK,
		Message: "ok",
		Data:    data,
	})
}

func respondCreated(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{
		Code:    domain.CodeOK,
		Message: "ok",
		Data:    data,
	})
}

func respondErr(c *gin.Context, err error) {
	var appErr *domain.AppError
	if errors.As(err, &appErr) {
		c.JSON(appErr.HTTPStatus, Envelope{
			Code:    appErr.Code,
			Message: appErr.Message,
		})
		return
	}

	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, Envelope{
			Code:    domain.CodeNotFound,
			Message: "resource not found",
		})
		return
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			c.JSON(http.StatusConflict, Envelope{
				Code:    domain.CodeConflict,
				Message: "resource already exists",
			})
			return
		case "23503":
			c.JSON(http.StatusBadRequest, Envelope{
				Code:    domain.CodeInvalidArgument,
				Message: "referenced resource does not exist or is still in use",
			})
			return
		case "23514":
			c.JSON(http.StatusBadRequest, Envelope{
				Code:    domain.CodeInvalidArgument,
				Message: "constraint validation failed",
			})
			return
		}
	}

	c.JSON(http.StatusInternalServerError, Envelope{
		Code:    domain.CodeInternal,
		Message: "internal server error",
	})
}
