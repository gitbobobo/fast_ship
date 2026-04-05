package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

type PaginatedData struct {
	Items    interface{} `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

func SuccessPaginated(c *gin.Context, items interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data: PaginatedData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

func Error(c *gin.Context, httpStatus int, code int, message string) {
	c.JSON(httpStatus, Response{
		Code:    code,
		Message: message,
		Data:    nil,
	})
}

func BadRequest(c *gin.Context, code int, message string) {
	Error(c, http.StatusBadRequest, code, message)
}

func Unauthorized(c *gin.Context, code int, message string) {
	Error(c, http.StatusUnauthorized, code, message)
}

func Forbidden(c *gin.Context, code int, message string) {
	Error(c, http.StatusForbidden, code, message)
}

func NotFound(c *gin.Context, code int, message string) {
	Error(c, http.StatusNotFound, code, message)
}

func Conflict(c *gin.Context, code int, message string) {
	Error(c, http.StatusConflict, code, message)
}

func InternalError(c *gin.Context, message string) {
	Error(c, http.StatusInternalServerError, 50000, message)
}
