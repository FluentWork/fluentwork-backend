package account

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := verifyPassword(encoded, "correct horse battery"); err != nil {
		t.Fatalf("verifyPassword on the same password: %v", err)
	}
}

func TestPasswordVerifyRejectsWrongPassword(t *testing.T) {
	encoded, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := verifyPassword(encoded, "correct horse batteru"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("want ErrPasswordMismatch, got %v", err)
	}
}

// 同一个口令两次哈希必须不同 —— 盐每次都要新取。
// 这条挡的是「把盐写成常量」那种最容易被顺手简化掉的错。
func TestPasswordHashIsSaltedPerCall(t *testing.T) {
	first, err := hashPassword("same password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	second, err := hashPassword("same password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if first == second {
		t.Fatal("同一个口令两次哈希得到同一个串 —— 盐没有每次新取")
	}
}

// 自描述的编码串：算法、迭代数、盐、摘要四段都在串里。
// 将来换算法时旧口令要还能验，这一条是那个承诺的前提。
func TestPasswordHashEncodingIsSelfDescribing(t *testing.T) {
	encoded, err := hashPassword("whatever")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		t.Fatalf("want 4 segments, got %d in %q", len(parts), encoded)
	}
	if parts[0] != passwordHashAlgorithm {
		t.Fatalf("want algorithm %q, got %q", passwordHashAlgorithm, parts[0])
	}
	if parts[1] != "210000" {
		t.Fatalf("want the iteration count in the string, got %q", parts[1])
	}
	if strings.Contains(encoded, " ") {
		t.Fatalf("哈希串里不该有空格：%q", encoded)
	}
}

func TestPasswordVerifyRejectsMalformedHash(t *testing.T) {
	cases := map[string]string{
		"段数不对":     "pbkdf2_sha256$210000$abc",
		"算法名不认":    "bcrypt$210000$YWJj$YWJj",
		"迭代数不是数":   "pbkdf2_sha256$notanumber$YWJj$YWJj",
		"迭代数为零":    "pbkdf2_sha256$0$YWJj$YWJj",
		"盐不是 base64": "pbkdf2_sha256$210000$!!$YWJj",
		"摘要不是 base64": "pbkdf2_sha256$210000$YWJj$!!",
		"摘要为空":     "pbkdf2_sha256$210000$YWJj$",
		"空串":       "",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			// 坏格式是**库里的数据坏了**，不是「用户口令错」——
			// 两者必须分得开，否则服务端的问题会被报成学员的问题。
			if err := verifyPassword(encoded, "whatever"); !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("want ErrInvalidPasswordHash, got %v", err)
			}
		})
	}
}

func TestValidatePasswordBounds(t *testing.T) {
	if err := validatePassword(strings.Repeat("a", MinPasswordLength)); err != nil {
		t.Fatalf("刚好下界的口令应当通过：%v", err)
	}
	if err := validatePassword(strings.Repeat("a", MaxPasswordLength)); err != nil {
		t.Fatalf("刚好上界的口令应当通过：%v", err)
	}
	if err := validatePassword(strings.Repeat("a", MinPasswordLength-1)); err == nil {
		t.Fatal("低于下界的口令应当被拒")
	}
	// 上界不是体验问题：PBKDF2 的输入越长越慢，没有上界等于给人一个拖住登录接口的入口。
	if err := validatePassword(strings.Repeat("a", MaxPasswordLength+1)); err == nil {
		t.Fatal("超过上界的口令应当被拒")
	}
}

func TestNormalizeEmailLowercasesAndTrims(t *testing.T) {
	got, err := normalizeEmail("  Tango@Example.COM ")
	if err != nil {
		t.Fatalf("normalizeEmail: %v", err)
	}
	if got != "tango@example.com" {
		t.Fatalf("want lowercased trimmed address, got %q", got)
	}
}

func TestNormalizeEmailRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "   ", "tango", "tango@", "@example.com", "a b@example.com"} {
		if _, err := normalizeEmail(raw); !errors.Is(err, ErrInvalidEmail) {
			t.Fatalf("want ErrInvalidEmail for %q, got %v", raw, err)
		}
	}
}
