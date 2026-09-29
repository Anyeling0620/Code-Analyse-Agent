package conversation

import (
	"edu.agent.code/service/dto"
	"sync"
)

// runBrokerBuffer 是单个订阅者的缓冲深度。
// 订阅者只是"推送通道"，事件真身在 chat_run_events 里，缓冲够用即可。
const runBrokerBuffer = 256

// maxRetainedClosedTopics 限制已关闭 topic 的保留数量。
// run 结束后 topic 会被标记关闭并短暂保留，避免"订阅晚于结束"的客户端
// 重新创建出一个永远不会再收到事件的活 topic；保留量到上限后按插入顺序清理。
const maxRetainedClosedTopics = 256

// runBroker 是进程内的 run 事件广播器。
//
// 它只负责"正在看这个 run 的客户端"的实时推送；事件本身由 chat_run_events 落库，
// 因此断连、漏推、客户端重连都能靠回放补齐，broker 不需要保证投递。
type runBroker struct {
	mu     sync.Mutex
	topics map[string]*runTopic
	// closedOrder 记录 topic 被关闭的先后顺序，用于清理。
	closedOrder []string
}

type runTopic struct {
	closed  bool
	nextSub int
	subs    map[int]*runSubscriber
}

type runSubscriber struct {
	ch   chan dto.ChatStreamEvent
	once sync.Once
}

func (s *runSubscriber) close() {
	s.once.Do(func() {
		close(s.ch)
	})
}

func newRunBroker() *runBroker {
	return &runBroker{topics: map[string]*runTopic{}}
}

func (b *runBroker) topicLocked(runID string) *runTopic {
	topic, ok := b.topics[runID]
	if !ok {
		topic = &runTopic{subs: map[int]*runSubscriber{}}
		b.topics[runID] = topic
	}
	return topic
}

// Subscribe 订阅某个 run 的实时事件。
//
// 返回的 cancel 必须被调用（defer），否则订阅者会一直挂在 topic 上。
// topic 已关闭时返回一个已关闭的 channel：调用方据此直接结束，不会阻塞等待。
func (b *runBroker) Subscribe(runID string) (<-chan dto.ChatStreamEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	topic := b.topicLocked(runID)
	if topic.closed {
		ch := make(chan dto.ChatStreamEvent)
		close(ch)
		return ch, func() {}
	}

	sub := &runSubscriber{ch: make(chan dto.ChatStreamEvent, runBrokerBuffer)}
	id := topic.nextSub
	topic.nextSub++
	topic.subs[id] = sub

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if cur, ok := b.topics[runID]; ok {
				if existing, ok := cur.subs[id]; ok {
					delete(cur.subs, id)
					existing.close()
				}
			}
			sub.close()
		})
	}
	return sub.ch, cancel
}

// Publish 把事件推给当前所有订阅者，永不阻塞。
//
// 缓冲写满说明这个客户端已经跟不上：直接关闭它的 channel，让它走
// "重连 + 按 Last-Event-ID 回放"的路径补事件，而不是拖慢 run 本身。
func (b *runBroker) Publish(runID string, event dto.ChatStreamEvent) {
	if runID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	topic, ok := b.topics[runID]
	if !ok {
		// 没有订阅者时也不必凭空创建 topic：事件已经落库，
		// 后续订阅者会通过回放拿到它。
		return
	}
	if topic.closed {
		return
	}
	for id, sub := range topic.subs {
		select {
		case sub.ch <- event:
		default:
			delete(topic.subs, id)
			sub.close()
		}
	}
}

// Close 关闭一个 run 的 topic：所有订阅者 channel 被关闭，后续订阅立刻返回已关闭 channel。
func (b *runBroker) Close(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	topic, ok := b.topics[runID]
	if !ok {
		topic = b.topicLocked(runID)
	}
	if topic.closed {
		return
	}
	topic.closed = true
	for id, sub := range topic.subs {
		delete(topic.subs, id)
		sub.close()
	}
	b.closedOrder = append(b.closedOrder, runID)
	b.evictClosedLocked()
}

func (b *runBroker) evictClosedLocked() {
	for len(b.closedOrder) > maxRetainedClosedTopics {
		oldest := b.closedOrder[0]
		b.closedOrder = b.closedOrder[1:]
		if topic, ok := b.topics[oldest]; ok && topic.closed && len(topic.subs) == 0 {
			delete(b.topics, oldest)
		}
	}
}
