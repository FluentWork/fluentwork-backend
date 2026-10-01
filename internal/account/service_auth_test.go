package account

import (
	"context"
	"errors"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func TestRegisterEmailCreatesRegisteredUser(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()

	tokens, err := svc.RegisterEmail(ctx, "tango@example.com", "a good password")
	if err != nil {
		t.Fatalf("RegisterEmail: %v", err)
	}
	if tokens.IsGuest {
		t.Fatal("注册出来的身份不该是游客")
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatal("注册应当同时签出令牌（建号即登录）")
	}

	// 库里那份必须是**注册用户**，而且口令是哈希不是明文。
	user, err := store.GetActiveByEmail(ctx, "tango@example.com")
	if err != nil {
		t.Fatalf("GetActiveByEmail: %v", err)
	}
	if user.IsGuest {
		t.Fatal("库里的 is_guest 应当为 false")
	}
	if user.PasswordHash == nil {
		t.Fatal("口令没有存下来")
	}
	if *user.PasswordHash == "a good password" {
		t.Fatal("库里存的是明文口令")
	}
}

func TestRegisterEmailRejectsDuplicate(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	if _, err := svc.RegisterEmail(ctx, "tango@example.com", "a good password"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := svc.RegisterEmail(ctx, "tango@example.com", "another password")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("want 409 conflict, got %v", err)
	}
}

// 邮箱是**小写存储**的口径：同一个邮箱的大小写变体是同一个人。
// 不归一的话，`Tango@example.com` 会绕过唯一键再建一个号 —— 而这个人下次用大写登录就进不去。
func TestRegisterEmailTreatsCaseVariantsAsTheSameAccount(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	if _, err := svc.RegisterEmail(ctx, "tango@example.com", "a good password"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := svc.RegisterEmail(ctx, "  Tango@Example.com ", "a good password")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("大小写变体应当撞上同一个账号，得到 %v", err)
	}
}

func TestLoginEmailReturnsTokens(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	if _, err := svc.RegisterEmail(ctx, "tango@example.com", "a good password"); err != nil {
		t.Fatalf("register: %v", err)
	}
	tokens, err := svc.LoginEmail(ctx, "  Tango@Example.COM ", "a good password")
	if err != nil {
		t.Fatalf("LoginEmail: %v", err)
	}
	if tokens.IsGuest {
		t.Fatal("登录出来的身份不该是游客")
	}
	if tokens.AccessToken == "" {
		t.Fatal("登录没有拿到访问令牌")
	}
}

// **这条是这一组里最要紧的判据**：口令不对与邮箱不存在必须给**完全一样**的错误。
//
// 只要它们分得开，登录接口就变成了一个免费的账号枚举器 ——
// 「这个邮箱注册过没有」本身就是一条不该被随便查的信息。
func TestLoginEmailDoesNotRevealWhetherTheAccountExists(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	if _, err := svc.RegisterEmail(ctx, "tango@example.com", "a good password"); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, errWrongPassword := svc.LoginEmail(ctx, "tango@example.com", "not the password")
	if errWrongPassword == nil {
		t.Fatal("口令不对却登录成功了")
	}
	_, errNoSuchAccount := svc.LoginEmail(ctx, "nobody@example.com", "not the password")
	if errNoSuchAccount == nil {
		t.Fatal("邮箱不存在却登录成功了")
	}

	var a, b *apierr.Error
	if !errors.As(errWrongPassword, &a) || !errors.As(errNoSuchAccount, &b) {
		t.Fatalf("两种失败都应当是 API 错误：%v / %v", errWrongPassword, errNoSuchAccount)
	}
	if a.Code != b.Code || a.Message != b.Message || a.HTTPStatus != b.HTTPStatus {
		t.Fatalf("两种失败暴露了账号是否存在：%+v vs %+v", a, b)
	}
}

// 登录**不校验口令规则**：规则是注册时的事。
// 规则收紧之后对老口令做校验，会把老用户挡在门外 —— 而他们什么也没做错。
func TestLoginEmailDoesNotApplyPasswordPolicy(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()

	// 直接写一个「按今天的规则不可能注册出来」的短口令用户，模拟规则收紧之前的老账号。
	short := "123"
	hash, err := hashPassword(short)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	email := "legacy@example.com"
	user := User{ID: "user-legacy", Email: &email, Status: UserStatusActive}
	user.PasswordHash = &hash
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := svc.LoginEmail(ctx, email, short); err != nil {
		t.Fatalf("老账号应当还能登录：%v", err)
	}
}

func TestRegisterEmailRejectsWeakPassword(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	_, err := svc.RegisterEmail(ctx, "tango@example.com", "short")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
		t.Fatalf("want 400 invalid argument, got %v", err)
	}
}

func TestRegisterEmailRejectsBadAddress(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	_, err := svc.RegisterEmail(ctx, "not-an-email", "a good password")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
		t.Fatalf("want 400 invalid argument, got %v", err)
	}
	// 登录对坏地址报的是**同一句**凭据错误（同样不泄露任何东西）。
	_, err = svc.LoginEmail(ctx, "not-an-email", "a good password")
	if !errors.As(err, &ae) || ae.HTTPStatus != 401 {
		t.Fatalf("want 401 on login with a malformed address, got %v", err)
	}
}

// 库里那份哈希坏了 **不是学员的错**：必须报服务端错误（500），
// 不然他会一直重试一个永远不会成功的口令。
func TestLoginEmailReportsCorruptHashAsServerError(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()

	broken := "pbkdf2_sha256$210000$!!!$!!!"
	email := "broken@example.com"
	user := User{ID: "user-broken", Email: &email, Status: UserStatusActive}
	user.PasswordHash = &broken
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err := svc.LoginEmail(ctx, email, "a good password")
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("want an API error, got %v", err)
	}
	if ae.HTTPStatus == 401 {
		t.Fatalf("把服务端的问题报成了「你密码错了」：%+v", ae)
	}
}

// 游客没有口令，也**不能**通过邮箱登录进来。
func TestLoginEmailRejectsGuestAccount(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()

	email := "guest-with-email@example.com"
	user := User{ID: "user-guest", Email: &email, IsGuest: true, Status: UserStatusActive}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err := svc.LoginEmail(ctx, email, "a good password")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 401 {
		t.Fatalf("want 401, got %v", err)
	}
}
