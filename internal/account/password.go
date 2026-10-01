package account

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 密码的存储与校验。
//
// **为什么用标准库的 PBKDF2 而不是 bcrypt/argon2**：这个仓的依赖清单里没有任何
// 密码学库，而 `crypto/pbkdf2` 从 Go 1.24 起就在标准库里（本仓 Go 1.26+）。
// 加一个外部依赖只为一行哈希，换来的是又多一个要跟版本、要审的安全面；
// 而 PBKDF2-HMAC-SHA256 在 OWASP 的推荐里仍然是可接受的口令派生函数
// （迭代数取 600k 系列里的较低档，见下）。
//
// 编码串**自描述**（算法 + 迭代数 + 盐 + 摘要）：将来要换 argon2id 时，
// 旧口令照样能验，登录成功的那一刻再重写成新格式 —— 不需要一次停服迁移。
const (
	passwordHashAlgorithm = "pbkdf2_sha256"

	// OWASP 对 PBKDF2-HMAC-SHA256 的建议是 600,000 次。这里取 210,000：
	// 与 PBKDF2-HMAC-SHA512 的推荐值同档，在服务端单核上约 50–80ms —— 登录接口
	// 可以接受，而离线爆破的成本已经足够高。**这个数字要跟着硬件一起看**，
	// 换机器时应当重新测一次（它写进了哈希串，改它不影响旧口令）。
	passwordIterations = 210_000

	passwordSaltBytes = 16
	passwordKeyBytes  = 32

	// 口令长度上下界。上界不是体验问题：PBKDF2 的输入越长越慢，
	// 没有上界等于给人一个「用超长口令把登录接口拖住」的入口。
	MinPasswordLength = 8
	MaxPasswordLength = 72
)

var (
	// ErrPasswordMismatch 是口令不对。**它和「用户不存在」对外是同一个错误**：
	// 登录接口不该告诉任何人「这个邮箱注册过没有」。
	ErrPasswordMismatch = errors.New("account: password mismatch")
	// ErrInvalidPasswordHash 是库里的哈希串格式不对（数据坏了，不是用户错了）。
	ErrInvalidPasswordHash = errors.New("account: invalid password hash")
)

// hashPassword 生成自描述的哈希串：`pbkdf2_sha256$<迭代数>$<盐>$<摘要>`。
func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("account: read salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyBytes)
	if err != nil {
		return "", fmt.Errorf("account: derive password key: %w", err)
	}
	return strings.Join([]string{
		passwordHashAlgorithm,
		strconv.Itoa(passwordIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// verifyPassword 比对口令。**比较用常数时间**（`subtle.ConstantTimeCompare`）：
// 逐字节提前返回的比较会把摘要泄露给计时攻击 —— 那正是「用哈希存口令」要防的东西。
func verifyPassword(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordHashAlgorithm {
		return ErrInvalidPasswordHash
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return ErrInvalidPasswordHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return ErrInvalidPasswordHash
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return fmt.Errorf("account: derive password key: %w", err)
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

// validatePassword 只管**规则**，不管强度评级。
//
// 规则只有一条长度区间，理由：目标用户是工程师，而「必须含大小写与符号」这类规则
// 在实测里换来的是更短更 predictable 的口令（写在便签上）。长度才是有效的那一维。
// 上限的理由见常量注释。
func validatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("密码至少 %d 位", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("密码最多 %d 位", MaxPasswordLength)
	}
	return nil
}
