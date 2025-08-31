package main

import (
	"fmt"
	"log"
	"strings"
)

// ----- logging with levels -----
const (
	levelDebug = iota + 1
	levelInfo
	levelWarn
	levelError
)

var currentLevel = levelInfo

func setLogLevel(s string) {
	switch strings.ToUpper(s) {
	case "DEBUG":
		currentLevel = levelDebug
	case "INFO":
		currentLevel = levelInfo
	case "WARNING", "WARN":
		currentLevel = levelWarn
	case "ERROR":
		currentLevel = levelError
	default:
		currentLevel = levelInfo
	}
}

func logDebug(fmtStr string, args ...any) {
	if currentLevel <= levelDebug {
		log.Printf("DEBUG "+fmtStr, args...)
	}
}
func logInfo(fmtStr string, args ...any) {
	if currentLevel <= levelInfo {
		log.Printf("INFO  "+fmtStr, args...)
	}
}
func logWarn(fmtStr string, args ...any) {
	if currentLevel <= levelWarn {
		log.Printf("WARN  "+fmtStr, args...)
	}
}
func logErr(fmtStr string, args ...any) {
	if currentLevel <= levelError {
		log.Printf("ERROR "+fmtStr, args...)
	}
}

// ----- small helpers -----
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
