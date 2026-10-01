package account

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// ErrInvalidEmail is returned when an email fails normalization.
var ErrInvalidEmail = errors.New("account: invalid email")

// RegisterEmail 创建一个「注册用户」（账号密码登录的第一步）。
//
// 它会**同时签出令牌**：注册成功后学员已经在登录态里了，不需要再登录一次。
// 这与 `IssueGuest` 的形状一致（建号即签令牌），客户端因而只有一条「拿到令牌」的路径。
func (s *Service) RegisterEmail(ctx context.Context, email, password string) (TokenResponse, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return TokenResponse{}, apierr.InvalidArgument("邮箱格式不对")
	}
	if err := validatePassword(password); err != nil {
		return TokenResponse{}, apierr.InvalidArgument(err.Error())
	}

	if _, err := s.store.GetActiveByEmail(ctx, normalized); err == nil {
		return TokenResponse{}, apierr.Conflict("这个邮箱已经注册过了")
	} else if !errors.Is(err, ErrNotFound) {
		return TokenResponse{}, err
	}

	hash, err := hashPassword(password)
	if err != nil {
		return TokenResponse{}, apierr.Internal("密码没能存下来")
	}

	now := s.now()
	user := User{
		ID:        s.newID(),
		Email:     &normalized,
		IsGuest:   false,
		Status:    UserStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	user.PasswordHash = &hash

	if err := s.store.CreateUser(ctx, user); err != nil {
		// 撞唯一键有两种可能：并发注册（真撞）或上面那次查询与这次写入之间的竞态。
		// 两种对调用方都是同一句话：这个邮箱已经在了。
		if errors.Is(err, ErrDuplicateEmail) {
			return TokenResponse{}, apierr.Conflict("这个邮箱已经注册过了")
		}
		return TokenResponse{}, err
	}

	s.logger.Info("🔑 Registered", "user_id", user.ID)
	return s.issueSession(ctx, user)
}

// LoginEmail 用邮箱 ＋ 口令换令牌。
//
// **两种失败给同一句话**（邮箱不存在 / 口令不对）：「这个邮箱注册过没有」本身就是
// 一条不该被随便查的信息 —— 分开报错等于给了一个免费的账号枚举接口。
func (s *Service) LoginEmail(ctx context.Context, email, password string) (TokenResponse, error) {
	// 登录**不做口令规则校验**：规则是注册时的事，这里只管「能不能对上」。
	// 对一个老口令做规则校验，会在规则收紧之后把老用户挡在门外。
	normalized, err := normalizeEmail(email)
	if err != nil {
		return TokenResponse{}, errCredentialRejected()
	}

	user, err := s.store.GetActiveByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// 用户不存在也**照样算一次哈希**：不然「不存在」这条路径快得多，
			// 计时差就成了枚举账号的第二个入口。这里用一个固定的假哈希走完同样的计算。
			_, _ = hashPassword(password)
			return TokenResponse{}, errCredentialRejected()
		}
		return TokenResponse{}, err
	}
	if user.IsGuest || user.PasswordHash == nil {
		// 游客没有口令。对外与「口令不对」同一句话。
		return TokenResponse{}, errCredentialRejected()
	}
	if err := verifyPassword(*user.PasswordHash, password); err != nil {
		if errors.Is(err, ErrInvalidPasswordHash) {
			// 库里那份坏了 —— 这是**服务端的问题**，不能报成「你密码错了」，
			// 否则学员会一直重试一个永远不会成功的口令。
			s.logger.Error("🔑 Stored password hash is invalid", "user_id", user.ID)
			return TokenResponse{}, apierr.Internal("登录服务暂时不可用")
		}
		return TokenResponse{}, errCredentialRejected()
	}

	s.logger.Info("🔑 Signed in", "user_id", user.ID)
	return s.issueSession(ctx, user)
}

func errCredentialRejected() error {
	return apierr.Unauthenticated("邮箱或密码不对")
}

// normalizeEmail 去空白 ＋ 转小写。
//
// 小写化是**存储口径**（users.email 的唯一键因此不需要大小写不敏感的排序规则），
// 而不是展示口径：真要用回原样，那是 profile 的事，不该让登录去记。
func normalizeEmail(email string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(email))
	if trimmed == "" {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || addr.Address != trimmed {
		return "", ErrInvalidEmail
	}
	return trimmed, nil
}
