package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sirupsen/logrus"
)

// Logger is a wrapper around logrus.Logger
type Logger struct {
	*logrus.Logger
	Component string
}

type LogLevel string

const (
	DebugLevel LogLevel = "debug"
	InfoLevel  LogLevel = "info"
	WarnLevel  LogLevel = "warn"
	ErrorLevel LogLevel = "error"
	FatalLevel LogLevel = "fatal"
	PanicLevel LogLevel = "panic"
)

type Config struct {
	Level     LogLevel
	Component string
	LogDir    string
	LogFile   string
}

func NewLogger(config Config) (*Logger, error) {
	logLevel, err := logrus.ParseLevel(string(config.Level))
	if err != nil {
		logLevel = logrus.InfoLevel
	}

	logger := logrus.New()
	logger.SetLevel(logLevel)

	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "level",
			logrus.FieldKeyMsg:   "message",
		},
	})

	if config.LogDir != "" && config.LogFile != "" {
		if err := os.MkdirAll(config.LogDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %v", err)
		}

		logFilePath := filepath.Join(config.LogDir, config.LogFile)
		file, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %v", err)
		}

		mw := io.MultiWriter(os.Stdout, file)
		logger.SetOutput(mw)
	}

	return &Logger{
		Logger:    logger,
		Component: config.Component,
	}, nil
}

func (l *Logger) WithFields(fields map[string]interface{}) *logrus.Entry {
	_, file, line, ok := runtime.Caller(1)
	if ok {
		fields["file"] = filepath.Base(file)
		fields["line"] = line
	}

	fields["component"] = l.Component

	return l.Logger.WithFields(fields)
}

func (l *Logger) Debug(msg string, fields map[string]interface{}) {
	l.WithFields(fields).Debug(msg)
}

func (l *Logger) Info(msg string, fields map[string]interface{}) {
	l.WithFields(fields).Info(msg)
}

func (l *Logger) Warn(msg string, fields map[string]interface{}) {
	l.WithFields(fields).Warn(msg)
}

func (l *Logger) Error(msg string, fields map[string]interface{}) {
	l.WithFields(fields).Error(msg)
}

func (l *Logger) Fatal(msg string, fields map[string]interface{}) {
	l.WithFields(fields).Fatal(msg)
}

func (l *Logger) Panic(msg string, fields map[string]interface{}) {
	l.WithFields(fields).Panic(msg)
}
