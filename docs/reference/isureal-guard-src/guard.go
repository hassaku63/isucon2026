package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"path"
	"strconv"
	"time"
)

const statsInterval = 30 * time.Second

// detector は IP ごとに、集計期間内の失敗の時刻を持つ。時刻はジャーナルの記録時刻を使う。
type detector struct {
	threshold int
	window    time.Duration
	failures  map[string][]time.Time
	lastSweep time.Time
}

func newDetector(threshold int, window time.Duration) *detector {
	return &detector{threshold: threshold, window: window, failures: make(map[string][]time.Time)}
}

// observe は ip のログイン失敗を記録し、BAN すべきなら true を返す。BAN したら数え直す。
func (d *detector) observe(ip string, at time.Time) bool {
	d.sweep(at)
	kept := d.failures[ip][:0]
	for _, failedAt := range d.failures[ip] {
		if at.Sub(failedAt) < d.window {
			kept = append(kept, failedAt)
		}
	}
	kept = append(kept, at)
	if len(kept) >= d.threshold {
		delete(d.failures, ip)
		return true
	}
	d.failures[ip] = kept
	return false
}

// sweep は集計期間ごとに、期間内の失敗がなくなった IP を捨てる。
func (d *detector) sweep(now time.Time) {
	if now.Sub(d.lastSweep) < d.window {
		return
	}
	d.lastSweep = now
	for ip, times := range d.failures {
		if now.Sub(times[len(times)-1]) >= d.window {
			delete(d.failures, ip)
		}
	}
}

type journalEntry struct {
	Message  json.RawMessage `json:"MESSAGE"`
	Realtime string          `json:"__REALTIME_TIMESTAMP"`
	Unit     string          `json:"_SYSTEMD_UNIT"`
}

const guardUnit = "isureal-guard.service"

func readsUnit(pattern, unit string) bool {
	if unit == guardUnit {
		return false
	}
	matched, err := path.Match(pattern, unit)
	return err == nil && matched
}

type actionLog struct {
	Action *string `json:"action"`
	Status int     `json:"status"`
	IP     string  `json:"ip"`
	Abuse  string  `json:"abuse"`
}

type failureKind string

const (
	loginFailureKind     failureKind = "login"
	invalidImageKind     failureKind = "invalid_image"
	invalidSignatureKind failureKind = "invalid_signature"
)

// parseFailure は、対象 unit のアクションログから BAN 対象の失敗を取り出す。
func parseFailure(line []byte, unitPattern string) (ip string, at time.Time, isAction bool, kind failureKind) {
	var entry journalEntry
	if err := json.Unmarshal(line, &entry); err != nil || !readsUnit(unitPattern, entry.Unit) {
		return "", time.Time{}, false, ""
	}
	// MESSAGE は UTF-8 でないとバイト列の配列になる。アクションログは ASCII なので文字列だけを読む。
	var message string
	if err := json.Unmarshal(entry.Message, &message); err != nil {
		return "", time.Time{}, false, ""
	}
	var action actionLog
	if err := json.Unmarshal([]byte(message), &action); err != nil || action.Action == nil {
		return "", time.Time{}, false, ""
	}
	if action.IP == "" {
		return "", time.Time{}, true, ""
	}
	switch {
	case *action.Action == "login" && action.Status == 401:
		kind = loginFailureKind
	case *action.Action == "put_profile_image" && action.Status == 400 && action.Abuse == string(invalidImageKind):
		kind = invalidImageKind
	case *action.Action == "get_image" && action.Status == 403 && action.Abuse == string(invalidSignatureKind):
		kind = invalidSignatureKind
	default:
		return "", time.Time{}, true, ""
	}
	micros, err := strconv.ParseInt(entry.Realtime, 10, 64)
	if err != nil {
		return "", time.Time{}, true, ""
	}
	return action.IP, time.UnixMicro(micros), true, kind
}

// run はジャーナルを 1 行ずつ読み、閾値を超えた IP を ban で BAN する。
func run(ctx context.Context, journal io.Reader, unitPattern string, d *detector, ban func(context.Context, string) error, logger *log.Logger) error {
	detectors := map[failureKind]*detector{
		loginFailureKind:     d,
		invalidImageKind:     newDetector(2, 5*time.Second),
		invalidSignatureKind: newDetector(3, 5*time.Second),
	}
	var lines, actions, failures, bans int
	nextStats := time.Now().Add(statsInterval)
	scanner := bufio.NewScanner(journal)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines++
		ip, at, isAction, kind := parseFailure(scanner.Bytes(), unitPattern)
		if isAction {
			actions++
		}
		if kind != "" {
			failures++
			detector := detectors[kind]
			if detector.observe(ip, at) {
				if err := ban(ctx, ip); err != nil {
					logger.Printf("ban failed: ip=%s reason=%s: %v", ip, kind, err)
				} else {
					bans++
					logger.Printf("banned: ip=%s reason=%s failures=%d window=%s", ip, kind, detector.threshold, detector.window)
				}
			}
		}
		if now := time.Now(); now.After(nextStats) {
			logger.Printf("stats: lines=%d action_logs=%d failures=%d bans=%d", lines, actions, failures, bans)
			nextStats = now.Add(statsInterval)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

type dbBanner struct {
	db       *sql.DB
	duration time.Duration
}

// ban は BAN の期限を DB の時刻で決める。すでに BAN されていれば遅い方の期限を残す。
func (b *dbBanner) ban(ctx context.Context, ip string) error {
	_, err := b.db.ExecContext(ctx, `
		INSERT INTO ip_bans (ip, banned_until)
		VALUES ($1, clock_timestamp() + make_interval(secs => $2))
		ON CONFLICT (ip) DO UPDATE SET banned_until = GREATEST(ip_bans.banned_until, EXCLUDED.banned_until)
	`, ip, b.duration.Seconds())
	return err
}
