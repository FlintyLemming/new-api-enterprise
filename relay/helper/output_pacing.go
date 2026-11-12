package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/tokenkit"

	"github.com/gin-gonic/gin"
)

const defaultOutputPacingMaxBufferMB = 16

var sseEventEnd = []byte("\n\n")

type pacingMode int

const (
	// pacingUndecided writes straight through until the response shows what
	// it is. A stream on an unpaced channel stays here, so a retry onto a
	// paced channel still starts pacing at its first event.
	pacingUndecided pacingMode = iota
	// pacingPassThrough writes straight through for the rest of the response.
	pacingPassThrough
	// pacingStream queues SSE events for the drain goroutine.
	pacingStream
	// pacingNonStream holds the whole body until Close.
	pacingNonStream
)

type pacingClock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type systemPacingClock struct{}

func (systemPacingClock) Now() time.Time { return time.Now() }

func (systemPacingClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pacedEvent is one SSE event waiting for its release. rate is the output
// rate of the channel that produced it, so a retry onto another channel paces
// its own events at its own rate.
type pacedEvent struct {
	data   []byte
	tokens int
	rate   int
}

// OutputPacer releases a channel's generated output to the client no faster
// than the channel's OutputTokensPerSecond.
//
// It sits directly above the client writer. Stream output is accepted at
// upstream speed into an in-memory queue, so the upstream connection is never
// slowed down, and a drain goroutine writes the queued SSE events on a token
// clock. A successful non-stream body is held whole and released once the
// time its output tokens need at the channel rate has passed since the
// request started. Close must run before the handler returns: it waits until
// everything accepted has reached the client or the client has gone.
type OutputPacer struct {
	// The client writer underneath. Once the drain goroutine runs it is the
	// only goroutine that writes to it.
	gin.ResponseWriter

	c           *gin.Context
	ctx         context.Context
	clock       pacingClock
	countTokens func(string) int
	start       time.Time
	maxBuffer   int

	mu   sync.Mutex
	cond *sync.Cond
	mode pacingMode
	// pending holds stream bytes since the last flush, or the whole held
	// non-stream body.
	pending     []byte
	queue       []pacedEvent
	queuedBytes int
	// baseSize and accepted make Size and Written report what the handler
	// wrote, without reading the client writer the drain goroutine updates.
	baseSize  int
	accepted  int
	committed bool
	closed    bool
	err       error
	drainDone chan struct{}
	budget    StreamWriteBudget
}

// StartOutputPacing installs an OutputPacer as c.Writer. The pacing rate is
// read from the selected channel's settings whenever output arrives.
func StartOutputPacing(c *gin.Context) *OutputPacer {
	model := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
	return startOutputPacing(c, systemPacingClock{}, func(text string) int {
		return tokenkit.Estimate(model, text)
	})
}

func startOutputPacing(c *gin.Context, clock pacingClock, countTokens func(string) int) *OutputPacer {
	maxBuffer := constant.OutputPacingMaxBufferMB << 20
	if maxBuffer <= 0 {
		maxBuffer = defaultOutputPacingMaxBufferMB << 20
	}
	p := &OutputPacer{
		ResponseWriter: c.Writer,
		c:              c,
		ctx:            c.Request.Context(),
		clock:          clock,
		countTokens:    countTokens,
		start:          clock.Now(),
		maxBuffer:      maxBuffer,
	}
	p.cond = sync.NewCond(&p.mu)
	c.Writer = p
	return p
}

func (p *OutputPacer) channelSetting() dto.ChannelSettings {
	setting, _ := common.GetContextKeyType[dto.ChannelSettings](p.c, constant.ContextKeyChannelSetting)
	return setting
}

// resolveModeLocked decides how the response is paced the first time body
// bytes or a flush arrive on a paced channel.
func (p *OutputPacer) resolveModeLocked() pacingMode {
	if p.mode != pacingUndecided {
		return p.mode
	}
	setting := p.channelSetting()
	paced := setting.OutputTokensPerSecond > 0
	if strings.HasPrefix(p.ResponseWriter.Header().Get("Content-Type"), "text/event-stream") {
		if !paced {
			return pacingUndecided
		}
		p.mode = pacingStream
	} else {
		status := p.ResponseWriter.Status()
		if !paced || !setting.OutputPacingNonStream || status < http.StatusOK || status >= http.StatusMultipleChoices {
			p.mode = pacingPassThrough
			return p.mode
		}
		p.mode = pacingNonStream
	}
	p.baseSize = p.ResponseWriter.Size()
	return p.mode
}

func (p *OutputPacer) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.resolveModeLocked() {
	case pacingStream:
		// Reading from upstream waits only once the queue is over its cap.
		// An empty queue always takes the next event, however large.
		for p.err == nil && p.queuedBytes > 0 && p.queuedBytes+len(p.pending)+len(data) > p.maxBuffer {
			p.cond.Wait()
		}
		if p.err != nil {
			return 0, p.err
		}
	case pacingNonStream:
	default:
		return p.ResponseWriter.Write(data)
	}
	p.pending = append(p.pending, data...)
	p.accepted += len(data)
	return len(data), nil
}

func (p *OutputPacer) WriteString(s string) (int, error) {
	return p.Write([]byte(s))
}

func (p *OutputPacer) Flush() {
	_ = p.FlushError()
}

// FlushError hands the flushed stream events to the drain goroutine and
// reports the client write error that stopped it, so FlushWriter callers still
// learn that the client is gone.
func (p *OutputPacer) FlushError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.resolveModeLocked() {
	case pacingStream:
		p.enqueuePendingLocked()
		return p.err
	case pacingNonStream:
		return nil
	default:
		p.ResponseWriter.Flush()
		return writerFlushError(p.ResponseWriter)
	}
}

// SetWriteDeadline is reached through http.ResponseController by writers
// stacked above. Paced output gets its deadline when it is actually written.
func (p *OutputPacer) SetWriteDeadline(deadline time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == pacingStream || p.mode == pacingNonStream {
		return nil
	}
	return http.NewResponseController(p.ResponseWriter).SetWriteDeadline(deadline)
}

func (p *OutputPacer) WriteHeader(code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.committed {
		return
	}
	p.ResponseWriter.WriteHeader(code)
}

func (p *OutputPacer) WriteHeaderNow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == pacingStream || p.mode == pacingNonStream {
		return
	}
	p.ResponseWriter.WriteHeaderNow()
}

func (p *OutputPacer) Status() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ResponseWriter.Status()
}

func (p *OutputPacer) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode != pacingStream && p.mode != pacingNonStream {
		return p.ResponseWriter.Size()
	}
	if p.accepted == 0 {
		return p.baseSize
	}
	return max(p.baseSize, 0) + p.accepted
}

func (p *OutputPacer) Written() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == pacingStream || p.mode == pacingNonStream {
		if p.committed || p.accepted > 0 {
			return true
		}
	}
	return p.ResponseWriter.Written()
}

// enqueuePendingLocked splits the flushed bytes into SSE events, prices each
// at the current channel's rate and wakes the drain goroutine.
func (p *OutputPacer) enqueuePendingLocked() {
	if len(p.pending) == 0 {
		return
	}
	if p.err != nil {
		p.pending = nil
		return
	}
	if !p.committed {
		// The headers go out from the handler goroutine, before the drain
		// goroutine exists, so the two never race on them.
		p.ResponseWriter.Flush()
		p.committed = true
	}
	rate := p.channelSetting().OutputTokensPerSecond
	for len(p.pending) > 0 {
		end := len(p.pending)
		if i := bytes.Index(p.pending, sseEventEnd); i >= 0 {
			end = i + len(sseEventEnd)
		}
		event := pacedEvent{data: p.pending[:end:end], rate: rate}
		if text := outputPacingEventText(event.data); text != "" {
			event.tokens = p.countTokens(text)
		}
		p.queue = append(p.queue, event)
		p.queuedBytes += end
		p.pending = p.pending[end:]
	}
	p.pending = nil
	if p.drainDone == nil {
		p.drainDone = make(chan struct{})
		go p.drain()
	}
	p.cond.Broadcast()
}

func (p *OutputPacer) failLocked(err error) {
	if p.err == nil {
		p.err = err
	}
	p.queue = nil
	p.queuedBytes = 0
	p.pending = nil
	p.cond.Broadcast()
}

// drain releases queued events in order. Each event goes out no earlier than
// the previous one plus the time its tokens take at its channel's rate. The
// clock restarts from now whenever upstream falls behind the rate, so a slow
// upstream is never delayed further and a pause never builds up a burst.
func (p *OutputPacer) drain() {
	defer func() {
		if recovered := recover(); recovered != nil {
			p.mu.Lock()
			p.failLocked(fmt.Errorf("output pacing drain panic: %v", recovered))
			p.mu.Unlock()
		}
		close(p.drainDone)
	}()

	var releaseAt time.Time
	for {
		p.mu.Lock()
		for len(p.queue) == 0 && !p.closed && p.err == nil {
			p.cond.Wait()
		}
		if len(p.queue) == 0 || p.err != nil {
			p.mu.Unlock()
			return
		}
		event := p.queue[0]
		p.queue[0] = pacedEvent{}
		p.queue = p.queue[1:]
		p.mu.Unlock()

		now := p.clock.Now()
		if releaseAt.Before(now) {
			releaseAt = now
		}
		if wait := releaseAt.Sub(now); wait > 0 {
			if err := p.clock.Sleep(p.ctx, wait); err != nil {
				p.mu.Lock()
				p.failLocked(err)
				p.mu.Unlock()
				return
			}
		}

		err := p.writeToClient(event.data)
		if err == nil {
			p.ResponseWriter.Flush()
			err = writerFlushError(p.ResponseWriter)
		}
		p.mu.Lock()
		p.queuedBytes -= len(event.data)
		if err != nil {
			p.failLocked(err)
			p.mu.Unlock()
			return
		}
		p.cond.Broadcast()
		p.mu.Unlock()

		if event.rate > 0 && event.tokens > 0 {
			releaseAt = releaseAt.Add(time.Duration(event.tokens) * time.Second / time.Duration(event.rate))
		}
	}
}

func (p *OutputPacer) writeToClient(data []byte) error {
	_ = http.NewResponseController(p.ResponseWriter).SetWriteDeadline(p.budget.Deadline(p.clock.Now(), len(data)))
	_, err := p.ResponseWriter.Write(data)
	return err
}

// Close sends whatever the handler left unflushed and waits until the paced
// output has reached the client or the client has gone. Later writes go
// straight to the client.
func (p *OutputPacer) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	mode := p.mode
	if mode == pacingStream {
		p.enqueuePendingLocked()
	}
	p.closed = true
	p.cond.Broadcast()
	drainDone := p.drainDone
	body := p.pending
	p.pending = nil
	p.mu.Unlock()

	if drainDone != nil {
		<-drainDone
	}
	if mode == pacingNonStream {
		p.releaseHeldBody(body)
	}

	p.mu.Lock()
	p.mode = pacingPassThrough
	p.mu.Unlock()
}

// releaseHeldBody writes a successful non-stream body once the time its
// output tokens need at the channel rate has passed since the request began.
// The output token count comes from the usage block of whichever client
// protocol the body uses.
func (p *OutputPacer) releaseHeldBody(body []byte) {
	var response struct {
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
		UsageMetadata struct {
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	rate := p.channelSetting().OutputTokensPerSecond
	if rate > 0 && common.Unmarshal(body, &response) == nil {
		tokens := response.Usage.CompletionTokens + response.Usage.OutputTokens +
			response.UsageMetadata.CandidatesTokenCount + response.UsageMetadata.ThoughtsTokenCount
		releaseAt := p.start.Add(time.Duration(tokens) * time.Second / time.Duration(rate))
		if wait := releaseAt.Sub(p.clock.Now()); wait > 0 {
			if p.clock.Sleep(p.ctx, wait) != nil {
				// The client left while the body was held.
				return
			}
		}
	}
	_ = p.writeToClient(body)
}

// outputPacingChunk names the fields that carry generated output in the
// stream chunks of the OpenAI chat and completions, Claude Messages, OpenAI
// Responses and Gemini protocols. Fields that repeat output already streamed,
// such as the Responses completed event, are deliberately absent.
type outputPacingChunk struct {
	// Delta is a string in Responses delta events and an object in Claude
	// content_block_delta events.
	Delta   json.RawMessage `json:"delta"`
	Choices []struct {
		Text  string `json:"text"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type outputPacingClaudeDelta struct {
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
}

// outputPacingEventText returns the generated text one SSE event carries:
// visible text, reasoning and tool call arguments. Events that carry none,
// such as pings, usage and [DONE], return "".
func outputPacingEventText(event []byte) string {
	var text strings.Builder
	for line := range bytes.SplitSeq(event, []byte("\n")) {
		payload, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 || payload[0] != '{' {
			continue
		}
		var chunk outputPacingChunk
		// A field of an unexpected type only drops that field; the rest of
		// the chunk is still decoded.
		_ = common.Unmarshal(payload, &chunk)
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Text)
			text.WriteString(choice.Delta.Content)
			text.WriteString(choice.Delta.ReasoningContent)
			text.WriteString(choice.Delta.Reasoning)
			for _, call := range choice.Delta.ToolCalls {
				text.WriteString(call.Function.Arguments)
			}
		}
		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				text.WriteString(part.Text)
			}
		}
		if len(chunk.Delta) == 0 {
			continue
		}
		switch chunk.Delta[0] {
		case '"':
			var delta string
			if common.Unmarshal(chunk.Delta, &delta) == nil {
				text.WriteString(delta)
			}
		case '{':
			var delta outputPacingClaudeDelta
			_ = common.Unmarshal(chunk.Delta, &delta)
			text.WriteString(delta.Text)
			text.WriteString(delta.Thinking)
			text.WriteString(delta.PartialJSON)
		}
	}
	return text.String()
}
