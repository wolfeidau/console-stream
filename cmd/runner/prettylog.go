// Package main provides a pretty slog handler for human-readable colored terminal output.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const (
	timeFormat = "[15:04:05.000]"
	reset      = "\033[0m"
)

// ANSI color codes
type Color int

const (
	Black Color = iota + 30
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	LightGray
	_
	_
	DarkGray     = 90
	LightRed     = 91
	LightGreen   = 92
	LightYellow  = 93
	LightBlue    = 94
	LightMagenta = 95
	LightCyan    = 96
	White        = 97
)

func (c Color) String() string {
	return fmt.Sprintf("\033[%dm", int(c))
}

func colorize(color Color, text string) string {
	return color.String() + text + reset
}

// getLevelColor returns the appropriate color for a log level
func getLevelColor(level slog.Level) Color {
	switch {
	case level <= slog.LevelDebug:
		return LightGray
	case level <= slog.LevelInfo:
		return Cyan
	case level < slog.LevelWarn:
		return LightBlue
	case level < slog.LevelError:
		return LightYellow
	case level <= slog.LevelError+1:
		return LightRed
	default:
		return LightMagenta
	}
}

// Handler is a slog.Handler that outputs human-readable colored logs
type Handler struct {
	h                slog.Handler
	r                func([]string, slog.Attr) slog.Attr
	b                *bytes.Buffer
	m                *sync.Mutex
	writer           io.Writer
	colorize         bool
	outputEmptyAttrs bool
}

// processAttr applies the ReplaceAttr function and returns the processed attribute
func (h *Handler) processAttr(key string, value slog.Value) slog.Attr {
	attr := slog.Attr{Key: key, Value: value}
	if h.r != nil {
		attr = h.r([]string{}, attr)
	}
	return attr
}

// colorizeIfEnabled applies color if colorization is enabled
func (h *Handler) colorizeIfEnabled(color Color, text string) string {
	if h.colorize {
		return colorize(color, text)
	}
	return text
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.h.Enabled(ctx, level)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{h: h.h.WithAttrs(attrs), b: h.b, r: h.r, m: h.m, writer: h.writer, colorize: h.colorize}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{h: h.h.WithGroup(name), b: h.b, r: h.r, m: h.m, writer: h.writer, colorize: h.colorize}
}

func (h *Handler) computeAttrs(
	ctx context.Context,
	r slog.Record,
) (map[string]any, error) {
	h.m.Lock()
	defer func() {
		h.b.Reset()
		h.m.Unlock()
	}()
	if err := h.h.Handle(ctx, r); err != nil {
		return nil, fmt.Errorf("error when calling inner handler's Handle: %w", err)
	}

	var attrs map[string]any
	err := json.Unmarshal(h.b.Bytes(), &attrs)
	if err != nil {
		return nil, fmt.Errorf("error when unmarshaling inner handler's Handle result: %w", err)
	}
	return attrs, nil
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Process level
	var level string
	levelAttr := h.processAttr(slog.LevelKey, slog.AnyValue(r.Level))
	if !levelAttr.Equal(slog.Attr{}) {
		level = h.colorizeIfEnabled(getLevelColor(r.Level), levelAttr.Value.String()+":")
	}

	// Process timestamp
	var timestamp string
	timeAttr := h.processAttr(slog.TimeKey, slog.StringValue(r.Time.Format(timeFormat)))
	if !timeAttr.Equal(slog.Attr{}) {
		timestamp = h.colorizeIfEnabled(LightGray, timeAttr.Value.String())
	}

	// Process message
	var msg string
	msgAttr := h.processAttr(slog.MessageKey, slog.StringValue(r.Message))
	if !msgAttr.Equal(slog.Attr{}) {
		msg = h.colorizeIfEnabled(White, msgAttr.Value.String())
	}

	// Process additional attributes
	attrs, err := h.computeAttrs(ctx, r)
	if err != nil {
		return fmt.Errorf("computing attributes: %w", err)
	}

	// Build output string
	var out strings.Builder
	if timestamp != "" {
		out.WriteString(timestamp + " ")
	}
	if level != "" {
		out.WriteString(level + " ")
	}
	if msg != "" {
		out.WriteString(msg + " ")
	}

	// Add attributes if present
	if h.outputEmptyAttrs || len(attrs) > 0 {
		attrsJSON, marshalErr := json.MarshalIndent(attrs, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("marshaling attributes: %w", marshalErr)
		}
		if len(attrsJSON) > 0 {
			out.WriteString(h.colorizeIfEnabled(LightBlue, string(attrsJSON)))
		}
	}

	out.WriteByte('\n')
	_, err = h.writer.Write([]byte(out.String()))
	return err
}

// suppressDefaults filters out standard slog attributes to prevent duplication
func suppressDefaults(next func([]string, slog.Attr) slog.Attr) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		// Skip standard fields that we handle separately
		if a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey {
			return slog.Attr{}
		}
		if next == nil {
			return a
		}
		return next(groups, a)
	}
}

// New creates a new pretty handler with custom options
func New(handlerOptions *slog.HandlerOptions, options ...Option) *Handler {
	if handlerOptions == nil {
		handlerOptions = &slog.HandlerOptions{}
	}

	buf := &bytes.Buffer{}
	handler := &Handler{
		b: buf,
		h: slog.NewJSONHandler(buf, &slog.HandlerOptions{
			Level:       handlerOptions.Level,
			AddSource:   handlerOptions.AddSource,
			ReplaceAttr: suppressDefaults(handlerOptions.ReplaceAttr),
		}),
		r: handlerOptions.ReplaceAttr,
		m: &sync.Mutex{},
	}

	for _, opt := range options {
		opt(handler)
	}

	return handler
}

// NewHandler creates a new pretty handler with sensible defaults for terminal output
func NewHandler(opts *slog.HandlerOptions) *Handler {
	return New(opts, WithDestinationWriter(os.Stdout), WithColor(), WithOutputEmptyAttrs())
}

// Option configures a Handler
type Option func(h *Handler)

// WithDestinationWriter sets the output writer
func WithDestinationWriter(writer io.Writer) Option {
	return func(h *Handler) {
		h.writer = writer
	}
}

// WithColor enables colored output
func WithColor() Option {
	return func(h *Handler) {
		h.colorize = true
	}
}

// WithOutputEmptyAttrs enables output even when no additional attributes are present
func WithOutputEmptyAttrs() Option {
	return func(h *Handler) {
		h.outputEmptyAttrs = true
	}
}
