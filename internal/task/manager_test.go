package task

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSubscribeHistoryReplay 验证任务执行期间新订阅者能拿到完整历史:
// 订阅前的事件在历史里,订阅后的事件走 channel,不重不漏。
func TestSubscribeHistoryReplay(t *testing.T) {
	m := &Manager{
		subs:    make(map[uint]map[uint]chan Event),
		history: make(map[uint][]Event),
	}

	const taskID = uint(100)
	// 订阅前广播 3 条(无人订阅,仅入历史)。
	for i := 0; i < 3; i++ {
		m.emit(taskID, EventLog, Event{Message: "old"})
	}

	subID, ch, hist := m.Subscribe(taskID)
	if len(hist) != 3 {
		t.Fatalf("历史长度 = %d, want 3", len(hist))
	}

	// 订阅后广播 2 条:应进历史并推送到 channel。
	m.emit(taskID, EventLog, Event{Message: "new-a"})
	m.emit(taskID, EventLog, Event{Message: "new-b"})

	// channel 里应只有订阅后的 2 条(非阻塞读)。
	got := drain(ch)
	if len(got) != 2 {
		t.Fatalf("channel 事件数 = %d, want 2", len(got))
	}
	if got[0].Message != "new-a" || got[1].Message != "new-b" {
		t.Fatalf("channel 事件内容不对: %v %v", got[0].Message, got[1].Message)
	}

	// 再次订阅:历史应包含全部 5 条(订阅期间的 2 条也入历史,无丢失无重复)。
	_, ch2, hist2 := m.Subscribe(taskID)
	if len(hist2) != 5 {
		t.Fatalf("第二次订阅历史长度 = %d, want 5", len(hist2))
	}
	got2 := drain(ch2)
	if len(got2) != 0 {
		t.Fatalf("第二个订阅不应收到历史事件,实际 %d 条", len(got2))
	}

	m.Unsubscribe(taskID, subID)
}

// TestHistoryCap 验证历史超过上限后丢弃最早的事件。
func TestHistoryCap(t *testing.T) {
	m := &Manager{
		subs:    make(map[uint]map[uint]chan Event),
		history: make(map[uint][]Event),
	}
	const taskID = uint(101)
	for i := 0; i < maxHistoryPerTask+50; i++ {
		m.emit(taskID, EventLog, Event{Message: time.Duration(i).String()})
	}
	_, _, hist := m.Subscribe(taskID)
	if len(hist) != maxHistoryPerTask {
		t.Fatalf("历史长度 = %d, want %d", len(hist), maxHistoryPerTask)
	}
	// 最早的事件应是第 50 条(前 50 条被丢弃)。
	if want := time.Duration(50).String(); hist[0].Message != want {
		t.Fatalf("历史首条 = %q, want %q", hist[0].Message, want)
	}
}

// TestClearCancelClearsHistory 验证任务结束时清掉历史。
func TestClearCancelClearsHistory(t *testing.T) {
	m := &Manager{
		subs:        make(map[uint]map[uint]chan Event),
		cancelFuncs: make(map[uint]context.CancelFunc),
		history:     make(map[uint][]Event),
	}
	const taskID = uint(102)
	m.emit(taskID, EventLog, Event{Message: "x"})
	m.RegisterCancel(taskID, func() {})
	m.ClearCancel(taskID)
	if _, ok := m.history[taskID]; ok {
		t.Fatal("ClearCancel 后历史应被清除")
	}
}

// TestConcurrentEmitSubscribe 并发广播 + 订阅的冒烟:不 panic、不死锁。
func TestConcurrentEmitSubscribe(t *testing.T) {
	m := &Manager{
		subs:    make(map[uint]map[uint]chan Event),
		history: make(map[uint][]Event),
	}
	const taskID = uint(103)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			m.emit(taskID, EventLog, Event{Message: "e"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			id, ch, _ := m.Subscribe(taskID)
			drain(ch)
			m.Unsubscribe(taskID, id)
		}
	}()
	wg.Wait()
}

// drain 非阻塞地取出 channel 中当前缓存的所有事件。
func drain(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}
