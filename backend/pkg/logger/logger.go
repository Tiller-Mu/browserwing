package logger

import (
	"context"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Logger interface {
	Warn(ctx context.Context, msg string, args ...any)
	Error(ctx context.Context, msg string, args ...any)
	Info(ctx context.Context, msg string, args ...any)
	Debug(ctx context.Context, msg string, args ...any)
}

type logrusLogger struct {
	logger *logrus.Logger
}

// getCallerFunctionName 获取调用者的函数名
func getCallerFunctionName() string {
	pc := make([]uintptr, 10)
	runtime.Callers(3, pc)
	funcName := runtime.FuncForPC(pc[0]).Name()
	// 提取最后一个点之后的部分作为函数名
	parts := strings.Split(funcName, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "unknown"
}

func (l *logrusLogger) Warn(ctx context.Context, msg string, args ...any) {
	entry := l.logger.WithContext(ctx)
	if traceID := getTraceID(ctx); traceID != "" {
		entry = entry.WithField("trace_id", traceID)
	}
	args = append([]any{getCallerFunctionName()}, args...)
	entry.Warnf("[%s] "+msg, args...)
}

func (l *logrusLogger) Error(ctx context.Context, msg string, args ...any) {
	entry := l.logger.WithContext(ctx)
	if traceID := getTraceID(ctx); traceID != "" {
		entry = entry.WithField("trace_id", traceID)
	}
	args = append([]any{getCallerFunctionName()}, args...)
	entry.Errorf("[%s] "+msg, args...)
}

func (l *logrusLogger) Info(ctx context.Context, msg string, args ...any) {
	entry := l.logger.WithContext(ctx)
	if traceID := getTraceID(ctx); traceID != "" {
		entry = entry.WithField("trace_id", traceID)
	}
	args = append([]any{getCallerFunctionName()}, args...)
	entry.Infof("[%s] "+msg, args...)
}

func (l *logrusLogger) Debug(ctx context.Context, msg string, args ...any) {
	entry := l.logger.WithContext(ctx)
	if traceID := getTraceID(ctx); traceID != "" {
		entry = entry.WithField("trace_id", traceID)
	}
	args = append([]any{getCallerFunctionName()}, args...)
	entry.Debugf("[%s] "+msg, args...)
}

var defaultLogger Logger

type LoggerConfig struct {
	Level      string `json:"level,omitempty" yaml:"level,omitempty" toml:"level,omitempty"`
	File       string `json:"file,omitempty" yaml:"file,omitempty" toml:"file,omitempty"`
	MaxSize    int    `json:"max_size,omitempty" yaml:"max_size,omitempty" toml:"max_size,omitempty"`          // 单个日志文件最大大小(MB),默认100MB
	MaxBackups int    `json:"max_backups,omitempty" yaml:"max_backups,omitempty" toml:"max_backups,omitempty"` // 保留的旧日志文件最大数量,默认3个
	MaxAge     int    `json:"max_age,omitempty" yaml:"max_age,omitempty" toml:"max_age,omitempty"`             // 保留旧日志文件的最大天数,默认7天
	Compress   bool   `json:"compress,omitempty" yaml:"compress,omitempty" toml:"compress,omitempty"`          // 是否压缩旧日志,默认false
}

func InitLogger(cfg *LoggerConfig) {
	log := logrus.New()
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	log.SetLevel(level)

	// 配置 JSON 格式,方便提取 trace_id
	log.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
	})

	if cfg.File != "" {
		// 设置默认值
		maxSize := cfg.MaxSize
		if maxSize <= 0 {
			maxSize = 100 // 默认 100MB
		}
		maxBackups := cfg.MaxBackups
		if maxBackups <= 0 {
			maxBackups = 3 // 默认保留 3 个备份
		}
		maxAge := cfg.MaxAge
		if maxAge <= 0 {
			maxAge = 7 // 默认保留 7 天
		}

		// 使用 lumberjack 实现日志轮转
		log.SetOutput(&lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   cfg.Compress,
		})
	}

	defaultLogger = &logrusLogger{logger: log}
}

func Warn(ctx context.Context, msg string, args ...any) {
	if defaultLogger == nil {
		return
	}
	defaultLogger.Warn(ctx, msg, args...)
}

func Error(ctx context.Context, msg string, args ...any) {
	if defaultLogger == nil {
		return
	}
	defaultLogger.Error(ctx, msg, args...)
}

func Info(ctx context.Context, msg string, args ...any) {
	if defaultLogger == nil {
		return
	}
	defaultLogger.Info(ctx, msg, args...)
}

func Debug(ctx context.Context, msg string, args ...any) {
	if defaultLogger == nil {
		return
	}
	defaultLogger.Debug(ctx, msg, args...)
}

func GetDefaultLogger() Logger {
	return defaultLogger
}

// TraceID context key
type contextKey string

const traceIDKey contextKey = "trace_id"

// WithTraceID 将 trace_id 添加到 context
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// getTraceID 从 context 中获取 trace_id
func getTraceID(ctx context.Context) string {
	if traceID, ok := ctx.Value(traceIDKey).(string); ok {
		return traceID
	}
	return ""
}

// GetTraceID 导出的获取 trace_id 函数
func GetTraceID(ctx context.Context) string {
	return getTraceID(ctx)
}
