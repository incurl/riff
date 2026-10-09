package email

import (
	"log/slog"

	"github.com/pkg/errors"
)

// asyncEmailRequest is the unit of work on the async queue.
type asyncEmailRequest struct {
	config  *Config
	message *Message
}

// asyncEmailQueue is a bounded chan that absorbs SendAsync calls
// without blocking the request goroutine. The buffer is 128,
// which matches the Memos upstream; an operator that floods the
// queue will see the drop warn and not stall the API.
var asyncEmailQueue = make(chan asyncEmailRequest, 128)

func init() {
	// Two workers match the upstream Memos setup. Each drains
	// the queue in order, calling Send synchronously per
	// message, and logs the failure mode on error. There is
	// no retry; an SMTP hiccup just shows up in the logs.
	for range 2 {
		go func() {
			for request := range asyncEmailQueue {
				if err := Send(request.config, request.message); err != nil {
					recipients := ""
					if request.message != nil && len(request.message.To) > 0 {
						recipients = request.message.To[0]
						if len(request.message.To) > 1 {
							recipients += " and others"
						}
					}
					slog.Warn("Failed to send email asynchronously",
						slog.String("recipients", recipients),
						slog.Any("error", err))
				}
			}
		}()
	}
}

// Send delivers a message synchronously. The error is wrapped
// with whatever context the caller passed in via the same
// function name in upstream Memos.
func Send(config *Config, message *Message) error {
	if config == nil {
		return errors.New("email configuration is required")
	}
	if message == nil {
		return errors.New("email message is required")
	}
	client := NewClient(config)
	return client.Send(message)
}

// SendAsync enqueues a message for bounded asynchronous
// delivery. The function returns immediately; the actual send
// happens in one of the workers started by init(). A full queue
// drops the message and logs a warn — the alternative (block
// the request goroutine) is strictly worse.
func SendAsync(config *Config, message *Message) {
	select {
	case asyncEmailQueue <- asyncEmailRequest{config: config, message: message}:
	default:
		slog.Warn("Dropped email because the async queue is full")
	}
}
