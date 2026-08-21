package task

import (
	"context"
	"sync"
	"time"

	"images-repo-sync/internal/config"
)

// EventType 是 SSE 推送的事件类型。
type EventType string

const (
	EventTaskStarted  EventType = "task_started"
	EventItemStarted  EventType = "item_started"
	EventItemProgress EventType = "item_progress"
	EventItemSuccess  EventType = "item_success"
	EventItemFailed   EventType = "item_failed"
	EventTaskFinished EventType = "task_finished"
	EventLog          EventType = "log"
)

// Event 是推送给 SSE 订阅者的事件。
type Event struct {
	Type      EventType `json:"type"`
	TaskID    uint      `json:"task_id"`
	ItemID    uint      `json:"item_id,omitempty"`
	SourceRef string    `json:"source_ref,omitempty"`
	TargetRef string    `json:"target_ref,omitempty"`
	Message   string    `json:"message,omitempty"`
	Data      any       `json:"data,omitempty"`
	Time      time.Time `json:"time"`
}

// Manager 负责任务的入队执行与 SSE 广播。
//
// 设计:
//   - tasks 队列是带缓冲 channel,若干 worker goroutine 消费(数量由 TASK_CONCURRENCY 配置,
//     默认 1 串行;skopeo 受 IO 限制,并发过大收益有限)。
//   - subscribers 按 taskID 维护一组 channel,任务执行时向所有订阅者广播 Event。
//   - 取消通过 cancel context 实现:Manager 记录每个运行中任务的 cancel func。
type Manager struct {
	queue       chan uint // 待执行任务 ID 队列
	mu          sync.Mutex
	subs        map[uint]map[uint]chan Event // taskID -> subscriberID -> chan
	cancelFuncs map[uint]context.CancelFunc  // 运行中任务的取消函数

	subIDCounter uint
}

var (
	instance *Manager
	once     sync.Once
)

// Instance 返回全局 Manager 单例。
func Instance() *Manager {
	once.Do(func() {
		instance = &Manager{
			queue:       make(chan uint, 64),
			subs:        make(map[uint]map[uint]chan Event),
			cancelFuncs: make(map[uint]context.CancelFunc),
		}
		// worker 数由 TASK_CONCURRENCY 控制(默认 1 串行,上限 8)。
		// 单个任务内的镜像仍串行复制;并发只作用于不同任务之间。
		for i := 0; i < config.AppConfig.TaskConcurrency; i++ {
			go instance.worker()
		}
	})
	return instance
}

// Enqueue 把任务 ID 投递到执行队列。
func (m *Manager) Enqueue(taskID uint) {
	select {
	case m.queue <- taskID:
	default:
		// 队列满,异步跑,避免阻塞 API。
		go func() { m.queue <- taskID }()
	}
}

// Subscribe 订阅某任务的实时事件流,返回订阅 id 与只读 channel。
// 用完后必须调用 Unsubscribe(taskID, subID) 释放。
func (m *Manager) Subscribe(taskID uint) (uint, <-chan Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.subIDCounter++
	subID := m.subIDCounter
	ch := make(chan Event, 64)
	if m.subs[taskID] == nil {
		m.subs[taskID] = make(map[uint]chan Event)
	}
	m.subs[taskID][subID] = ch
	return subID, ch
}

// Unsubscribe 释放订阅。
func (m *Manager) Unsubscribe(taskID, subID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if subs, ok := m.subs[taskID]; ok {
		if ch, ok := subs[subID]; ok {
			delete(subs, subID)
			close(ch)
		}
		if len(subs) == 0 {
			delete(m.subs, taskID)
		}
	}
}

// broadcast 向某任务的所有订阅者广播事件(非阻塞,慢消费者会被跳过)。
func (m *Manager) broadcast(taskID uint, ev Event) {
	m.mu.Lock()
	subs := m.subs[taskID]
	// 复制订阅者列表,避免持锁写 channel。
	channels := make([]chan Event, 0, len(subs))
	for _, ch := range subs {
		channels = append(channels, ch)
	}
	m.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- ev:
		default:
			// 订阅者落后,丢弃这条以保护任务执行不被阻塞。
		}
	}
}

// RegisterCancel 记录某运行中任务的取消函数。
func (m *Manager) RegisterCancel(taskID uint, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelFuncs[taskID] = cancel
}

// ClearCancel 清除某任务的取消函数(任务结束)。
func (m *Manager) ClearCancel(taskID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancelFuncs, taskID)
}

// Cancel 取消某运行中任务。返回是否成功发送了取消信号。
func (m *Manager) Cancel(taskID uint) bool {
	m.mu.Lock()
	cancel, ok := m.cancelFuncs[taskID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// emit 是 broadcast 的便捷封装,自动填充 TaskID 与 Time。
func (m *Manager) emit(taskID uint, typ EventType, payload Event) {
	payload.Type = typ
	payload.TaskID = taskID
	if payload.Time.IsZero() {
		payload.Time = time.Now()
	}
	m.broadcast(taskID, payload)
}
