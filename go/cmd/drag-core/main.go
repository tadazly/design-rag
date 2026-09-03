package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/tadazly/design-rag/go/core"
)

type emitter struct {
	mutex   sync.Mutex
	encoder *json.Encoder
}

func (output *emitter) send(value any) error {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.encoder.Encode(value)
}

type inputMessage struct {
	Type            string          `json:"type"`
	ID              string          `json:"id"`
	Method          string          `json:"method"`
	ProtocolVersion int             `json:"protocolVersion"`
	Command         string          `json:"command"`
	Payload         json.RawMessage `json:"payload"`
	OK              bool            `json:"ok"`
	Draft           *core.Draft     `json:"draft"`
	Error           string          `json:"error"`
}

type fallbackResponse struct {
	draft *core.Draft
	err   error
}

type fallbackBroker struct {
	output  *emitter
	mutex   sync.Mutex
	pending map[string]chan fallbackResponse
	nextID  atomic.Uint64
}

func newFallbackBroker(output *emitter) *fallbackBroker {
	return &fallbackBroker{output: output, pending: map[string]chan fallbackResponse{}}
}

func (broker *fallbackBroker) Extract(ctx context.Context, candidate core.Candidate, existingContentHash string, full bool) (*core.Draft, error) {
	id := fmt.Sprintf("fallback-%d", broker.nextID.Add(1))
	response := make(chan fallbackResponse, 1)
	broker.mutex.Lock()
	broker.pending[id] = response
	broker.mutex.Unlock()
	defer func() {
		broker.mutex.Lock()
		delete(broker.pending, id)
		broker.mutex.Unlock()
	}()
	if err := broker.output.send(map[string]any{
		"type": "fallback_request",
		"id":   id,
		"input": map[string]any{
			"candidate":           candidate,
			"existingContentHash": existingContentHash,
			"full":                full,
		},
	}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-response:
		return result.draft, result.err
	}
}

func (broker *fallbackBroker) resolve(message inputMessage) {
	broker.mutex.Lock()
	channel := broker.pending[message.ID]
	broker.mutex.Unlock()
	if channel == nil {
		return
	}
	result := fallbackResponse{draft: message.Draft}
	if !message.OK {
		if message.Error == "" {
			message.Error = "TypeScript fallback extractor 返回失败"
		}
		result.err = errors.New(message.Error)
	} else if message.Draft == nil {
		result.err = errors.New("TypeScript fallback extractor 未返回 draft")
	}
	select {
	case channel <- result:
	default:
	}
}

func writeVersion(jsonOutput bool) {
	value := map[string]any{
		"name":            "drag-core",
		"version":         core.BackendVersion,
		"protocolVersion": core.ProtocolVersion,
		"go":              runtime.Version(),
		"platform":        runtime.GOOS,
		"arch":            runtime.GOARCH,
	}
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(value)
		return
	}
	fmt.Fprintf(os.Stdout, "drag-core %s (protocol %d, %s/%s)\n", core.BackendVersion, core.ProtocolVersion, runtime.GOOS, runtime.GOARCH)
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	for _, argument := range os.Args[1:] {
		if argument == "--version" || argument == "version" {
			writeVersion(slicesContain(os.Args[1:], "--json"))
			return
		}
	}
	if err := serve(); err != nil {
		log.Printf("drag-core failed: %v", err)
		os.Exit(1)
	}
}

func slicesContain(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func serve() error {
	output := &emitter{encoder: json.NewEncoder(os.Stdout)}
	if err := output.send(map[string]any{
		"type":            "hello",
		"protocolVersion": core.ProtocolVersion,
		"backendVersion":  core.BackendVersion,
		"pid":             os.Getpid(),
		"platform":        runtime.GOOS,
		"arch":            runtime.GOARCH,
		"capabilities":    append(core.RuntimeCapabilities(), "cancel", "progress"),
	}); err != nil {
		return err
	}

	controller := core.NewController()
	broker := newFallbackBroker(output)
	requestChannel := make(chan inputMessage, 1)
	decoderError := make(chan error, 1)
	rootContext, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		decoder := json.NewDecoder(os.Stdin)
		for {
			var message inputMessage
			if err := decoder.Decode(&message); err != nil {
				decoderError <- err
				return
			}
			switch strings.ToLower(message.Type) {
			case "request":
				select {
				case requestChannel <- message:
				default:
					_ = output.send(map[string]any{"type": "error", "code": "request_already_running", "message": "drag-core 每个进程只接受一个 index 请求"})
				}
			case "control":
				switch strings.ToLower(message.Command) {
				case "pause":
					changed := controller.Pause()
					_ = output.send(map[string]any{"type": "control_ack", "command": "pause", "changed": changed})
				case "resume":
					changed := controller.Resume()
					_ = output.send(map[string]any{"type": "control_ack", "command": "resume", "changed": changed})
				case "cancel":
					cancel()
					_ = output.send(map[string]any{"type": "control_ack", "command": "cancel", "changed": true})
				default:
					_ = output.send(map[string]any{"type": "error", "code": "unknown_control", "message": "未知 control command"})
				}
			case "fallback_result":
				broker.resolve(message)
			default:
				_ = output.send(map[string]any{"type": "error", "code": "unknown_message", "message": "未知输入消息类型"})
			}
		}
	}()

	var requestMessage inputMessage
	select {
	case <-rootContext.Done():
		return rootContext.Err()
	case err := <-decoderError:
		return fmt.Errorf("读取首个协议请求失败: %w", err)
	case requestMessage = <-requestChannel:
	}
	if requestMessage.ProtocolVersion != core.ProtocolVersion {
		message := fmt.Sprintf("协议版本不兼容：host=%d backend=%d", requestMessage.ProtocolVersion, core.ProtocolVersion)
		_ = output.send(map[string]any{"type": "error", "id": requestMessage.ID, "code": "protocol_version_mismatch", "message": message})
		return errors.New(message)
	}
	if requestMessage.Method != "index" {
		message := "drag-core 当前只接受 index method"
		_ = output.send(map[string]any{"type": "error", "id": requestMessage.ID, "code": "unknown_method", "message": message})
		return errors.New(message)
	}
	var request core.IndexRequest
	if err := json.Unmarshal(requestMessage.Payload, &request); err != nil {
		_ = output.send(map[string]any{"type": "error", "id": requestMessage.ID, "code": "invalid_request", "message": err.Error()})
		return err
	}
	summary, metrics, err := core.RunIndex(rootContext, request, controller, broker, func(progress core.RunSummary) {
		_ = output.send(map[string]any{"type": "progress", "id": requestMessage.ID, "summary": progress})
	})
	if err != nil {
		_ = output.send(map[string]any{"type": "error", "id": requestMessage.ID, "code": "index_failed", "message": err.Error(), "summary": summary, "metrics": metrics})
		return err
	}
	return output.send(map[string]any{"type": "result", "id": requestMessage.ID, "summary": summary, "metrics": metrics})
}
