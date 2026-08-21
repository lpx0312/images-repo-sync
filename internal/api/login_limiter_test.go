package api

import (
	"testing"
	"time"
)

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	userKey, ipKey := "user:admin", "ip:1.2.3.4"

	// 阈值减一次失败:不应锁定。
	for i := 0; i < loginMaxUserFails-1; i++ {
		l.recordFailure(userKey, ipKey)
	}
	if locked, _ := l.locked(userKey, ipKey); locked {
		t.Fatal("未达用户阈值不应锁定")
	}

	// 达到用户阈值(5 次):锁定并给出剩余时长。
	l.recordFailure(userKey, ipKey)
	locked, wait := l.locked(userKey, ipKey)
	if !locked || wait <= 0 || wait > loginLockDuration {
		t.Fatalf("达用户阈值应锁定 10 分钟, locked=%v wait=%v", locked, wait)
	}

	// 锁定期间继续失败不应延长锁定期。
	time.Sleep(10 * time.Millisecond)
	l.recordFailure(userKey, ipKey)
	if locked2, wait2 := l.locked(userKey, ipKey); !locked2 || wait2 > wait {
		t.Fatalf("锁定期间失败不应延长, wait=%v wait2=%v", wait, wait2)
	}

	// 成功登录清零。
	l.reset(userKey, ipKey)
	if locked, _ := l.locked(userKey, ipKey); locked {
		t.Fatal("reset 后不应再锁定")
	}

	// IP 维度阈值(10 次)独立生效。
	ipOnly := "ip:9.9.9.9"
	for i := 0; i < loginMaxIPFails; i++ {
		l.recordFailure(ipOnly)
	}
	if locked, _ := l.locked(ipOnly); !locked {
		t.Fatal("达 IP 阈值应锁定")
	}

	// 未涉及的 key 不受影响。
	if locked, _ := l.locked("user:someone-else"); locked {
		t.Fatal("无关 key 不应被锁定")
	}
}
