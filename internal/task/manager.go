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

// maxHistoryPerTask 单个任务保留的事件历史上限,超出后丢弃最早的事件。
// 与前端日志行数上限(2000)对齐;每条事件几百字节,内存可控。
const maxHistoryPerTask = 2000

// Manager 负责任务的入队执行与 SSE 广播。
//
// 设计:
//   - tasks 队列是带缓冲 channel,若干 worker goroutine 消费(数量由 TASK_CONCURRENCY 配置,
//     默认 1 串行;skopeo 受 IO 限制,并发过大收益有限)。
//   - subscribers 按 taskID 维护一组 channel,任务执行时向所有订阅者广播 Event。
//   - history 按 taskID 保留已广播事件的副本(仅任务执行期间):
//     任务运行中页面刷新会重连 SSE,新订阅者需要先拿到此前的事件才能续上日志,
//     否则实时日志清空。任务结束时清除(已结束任务的日志由 API 从数据库回放)。
//   - 取消通过 cancel context 实现:Manager 记录每个运行中任务的 cancel func。
type Manager struct {
	queue       chan uint // 待执行任务 ID 队列
	mu          sync.Mutex
	subs        map[uint]map[uint]chan Event // taskID -> subscriberID -> chan
	cancelFuncs map[uint]context.CancelFunc  // 运行中任务的取消函数
	history     map[uint][]Event             // taskID -> 已广播事件历史(任务执行期间)

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
			history:     make(map[uint][]Event),
		}
		// worker 数由 TASK_CONCURRENCY 控制(默认 1 串行,上限 8)。
		// 单个任务内的镜像仍串行复制;并发只作用于不同任务之间。
		// 下限 1:防止 config 未加载(concurrency 为 0)时静默起 0 个 worker、任务永远无人消费。
		workers := config.AppConfig.TaskConcurrency
		if workers < 1 {
			workers = 1
		}
		for i := 0; i < workers; i++ {
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

// Subscribe 订阅某任务的实时事件流,返回订阅 id、只读 channel 与当前的历史事件快照。
// 用完后必须调用 Unsubscribe(taskID, subID) 释放。
//
// 历史快照与订阅注册在同一临界区内完成:注册前的事件在历史里(回放给调用方),
// 之后的事件进 channel,不重不漏。调用方应先消费历史再消费 channel。
func (m *Manager) Subscribe(taskID uint) (uint, <-chan Event, []Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.subIDCounter++
	subID := m.subIDCounter
	ch := make(chan Event, 64)
	if m.subs[taskID] == nil {
		m.subs[taskID] = make(map[uint]chan Event)
	}
	m.subs[taskID][subID] = ch
	hist := make([]Event, len(m.history[taskID]))
	copy(hist, m.history[taskID])
	return subID, ch, hist
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

// broadcast 向某任务的所有订阅者广播事件,同时把事件追加进历史,
// 供任务执行期间新接入的订阅者回放(页面刷新后实时日志不丢)。
//
// 发送在锁内进行且为非阻塞(select 带 default,永不会阻塞):
// 若在锁外发送,会与 Unsubscribe 的 close(ch) 竞态,触发 send on closed channel panic
// (任务运行中客户端断开 SSE 时即可能发生)。
func (m *Manager) broadcast(taskID uint, ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 记录历史(环形截断,保留最近 maxHistoryPerTask 条)。
	h := append(m.history[taskID], ev)
	if len(h) > maxHistoryPerTask {
		h = h[len(h)-maxHistoryPerTask:]
	}
	m.history[taskID] = h
	// 锁内非阻塞发送:与 Unsubscribe 的注册表变更互斥,杜绝向已关闭 channel 发送。
	for _, ch := range m.subs[taskID] {
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

// ClearCancel 清除某任务的取消函数(任务结束),同时清掉事件历史:
// 任务已结束,后续接入的客户端由 API 从数据库回放日志,内存历史不再需要。
func (m *Manager) ClearCancel(taskID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancelFuncs, taskID)
	delete(m.history, taskID)
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
