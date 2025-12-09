package logger

import (
	"fmt"
	"log"
	"os"
	"time"
)

// Logger levels
const (
	LevelDebug = "DEBUG"
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// Logger provides structured logging
type Logger struct {
	prefix string
}

// New creates a new logger with optional prefix
func New(prefix string) *Logger {
	return &Logger{prefix: prefix}
}

// Default logger instance
var defaultLogger = New("")

// formatMessage formats a log message with timestamp, level, and fields
func (l *Logger) formatMessage(level, message string, fields map[string]interface{}) string {
	timestamp := time.Now().Format("2006/01/02 15:04:05")

	prefix := ""
	if l.prefix != "" {
		prefix = fmt.Sprintf("[%s] ", l.prefix)
	}

	msg := fmt.Sprintf("%s [%s] %s%s", timestamp, level, prefix, message)

	// Append fields if provided
	if len(fields) > 0 {
		for k, v := range fields {
			msg += fmt.Sprintf(" %s=%v", k, v)
		}
	}

	return msg
}

// Debug logs a debug message
func (l *Logger) Debug(message string, fields map[string]interface{}) {
	log.Println(l.formatMessage(LevelDebug, message, fields))
}

// Info logs an info message
func (l *Logger) Info(message string, fields map[string]interface{}) {
	log.Println(l.formatMessage(LevelInfo, message, fields))
}

// Warn logs a warning message
func (l *Logger) Warn(message string, fields map[string]interface{}) {
	log.Println(l.formatMessage(LevelWarn, message, fields))
}

// Error logs an error message
func (l *Logger) Error(message string, fields map[string]interface{}) {
	log.Println(l.formatMessage(LevelError, message, fields))
}

// Global logging functions using default logger

// Debug logs a debug message using default logger
func Debug(message string, fields map[string]interface{}) {
	defaultLogger.Debug(message, fields)
}

// Info logs an info message using default logger
func Info(message string, fields map[string]interface{}) {
	defaultLogger.Info(message, fields)
}

// Warn logs a warning message using default logger
func Warn(message string, fields map[string]interface{}) {
	defaultLogger.Warn(message, fields)
}

// Error logs an error message using default logger
func Error(message string, fields map[string]interface{}) {
	defaultLogger.Error(message, fields)
}

// Fatal logs a fatal error and exits
func Fatal(message string, fields map[string]interface{}) {
	log.Println(defaultLogger.formatMessage(LevelError, "FATAL: "+message, fields))
	os.Exit(1)
}

// Simple convenience functions for common use cases

// Infof logs an info message with Printf-style formatting
func Infof(format string, args ...interface{}) {
	Info(fmt.Sprintf(format, args...), nil)
}

// Warnf logs a warning message with Printf-style formatting
func Warnf(format string, args ...interface{}) {
	Warn(fmt.Sprintf(format, args...), nil)
}

// Errorf logs an error message with Printf-style formatting
func Errorf(format string, args ...interface{}) {
	Error(fmt.Sprintf(format, args...), nil)
}
