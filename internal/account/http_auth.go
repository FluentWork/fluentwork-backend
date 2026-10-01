package account

import (
	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// RegisterRequest is the POST /auth/register body.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginRequest is the POST /auth/login body.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// PostRegister handles POST /auth/register.
//
// 请求体用 `ShouldBindJSON` 绑成 `string` 而不是加 `binding:"required"` 标签：
// 空值该由服务层的规则去说（「邮箱格式不对」/「密码至少 8 位」），
// 而不是让 gin 回一句 `Key: 'RegisterRequest.Email' Error:Field validation ...` ——
// 那句话是给开发看的，不是给学员看的。
func (h *Handler) PostRegister(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("请求格式不对"))
		return
	}
	result, err := h.svc.RegisterEmail(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostLogin handles POST /auth/login.
func (h *Handler) PostLogin(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 注意：解析失败**不是**凭据错误。报文坏了与「邮箱或密码不对」是两件事，
		// 混成一句会让排查的人从错的方向找。
		httpjson.Error(c, apierr.InvalidArgument("请求格式不对"))
		return
	}
	result, err := h.svc.LoginEmail(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}
