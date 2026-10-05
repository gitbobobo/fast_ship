package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
)

// Response 是全部 API 路由的统一 JSON 信封 {code, message, data}；
// data 带静态类型，约束序列化出去的负载与 spec 中的类型一致。
type Response[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// PaginatedData 是分页响应的 data 形状。
type PaginatedData[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

func Success[T any](c *gin.Context, data T) {
	c.JSON(http.StatusOK, Response[T]{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// SuccessEmpty 发出 {"code":0,"message":"success","data":null}；
// 独立函数是因为泛型无法从 nil 实参推导 T。
func SuccessEmpty(c *gin.Context) {
	Success[any](c, nil)
}

func SuccessPaginated[T any](c *gin.Context, items []T, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, Response[PaginatedData[T]]{
		Code:    0,
		Message: "success",
		Data: PaginatedData[T]{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

func Error(c *gin.Context, httpStatus int, code int, message string) {
	c.JSON(httpStatus, api.ErrorEnvelope{
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
