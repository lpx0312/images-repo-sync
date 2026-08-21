package api

import (
	"strings"
	"sync"
	"time"

	"images-repo-sync/internal/auth"
)

// 登录失败锁定阈值:同一用户名连续失败 5 次、同一 IP 连续失败 10 次,锁定 10 分钟。
// 内存实现(重启清零),对单实例部署的内网工具足够。
const (
	loginMaxUserFails = 5
	loginMaxIPFails   = 10
	loginLockDuration = 10 * time.Minute
)

// loginLimiter 按 key(形如 "user:alice" / "ip:1.2.3.4")累计登录失败次数,
// 达到阈值后锁定一段时间;成功登录清零对应 key。
type loginLimiter struct {
	mu      sync.Mutex
	records map[string]*loginFailRecord
}

type loginFailRecord struct {
	fails       int
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{records: make(map[string]*loginFailRecord)}
}

// locked 返回这组 key 中是否任一仍处于锁定期,以及剩余等待时长(未锁定为零值)。
func (l *loginLimiter) locked(keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var remain time.Duration
	for _, k := range keys {
		r, ok := l.records[k]
		if !ok {
			continue
		}
		if d := r.lockedUntil.Sub(time.Now()); d > remain {
			remain = d
		}
	}
	return remain > 0, remain
}

// recordFailure 对每个 key 按其阈值累计失败;达到阈值即锁定并清零计数
// (解锁后重新累计,不会永久累加)。
func (l *loginLimiter) recordFailure(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		max := loginMaxUserFails
		if strings.HasPrefix(k, "ip:") {
			max = loginMaxIPFails
		}
		r, ok := l.records[k]
		if !ok {
			r = &loginFailRecord{}
			l.records[k] = r
		}
		if r.lockedUntil.After(now) {
			continue // 已锁定,不再累计。
		}
		r.fails++
		if r.fails >= max {
			r.lockedUntil = now.Add(loginLockDuration)
			r.fails = 0
		}
	}
}

// reset 清除这组 key 的失败记录(登录成功时调用)。
func (l *loginLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.records, k)
	}
}

// loginGuard 是登录接口共用的全局限速器。
var loginGuard = newLoginLimiter()

// loginDummyHash 用于「用户不存在」分支的等耗时 bcrypt 比较,
// 消除与「密码错误」分支的响应时序差,防止用户名枚举。
// 包初始化时计算一次(约几十毫秒),内容本身无意义。
var loginDummyHash, _ = auth.HashPassword("login-timing-equalizer")
