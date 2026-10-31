// isureal-guard は、アプリのアクションログを journalctl で読み、攻撃を繰り返す IP アドレスを
// ip_bans テーブルへ書いて BAN する。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strconv"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const usage = `Usage: isureal-guard

isureal-guard reads the action log of systemd units through journalctl and bans an IP address
when it fails to log in GUARD_FAIL_THRESHOLD times within GUARD_FAIL_WINDOW, sends two invalid
profile images within five seconds, or requests an image with an invalid signature three times
within five seconds. A ban is written to the ip_bans table
and lasts GUARD_BAN_DURATION.

Environment variables:
  GUARD_JOURNAL_UNIT    pattern of systemd units whose log is read; isureal-guard.service is
                        never read (default: isureal-*.service)
  GUARD_FAIL_THRESHOLD  failed logins from one IP address that trigger a ban (default: 10)
  GUARD_FAIL_WINDOW     period in which failed logins are counted (default: 10s)
  GUARD_BAN_DURATION    how long a ban lasts (default: 2m)
  ISUREAL_DB_HOST       database host (default: 127.0.0.1)
  ISUREAL_DB_PORT       database port (default: 5432)
  ISUREAL_DB_USER       database user (default: isureal)
  ISUREAL_DB_PASSWORD   database password (default: isureal)
  ISUREAL_DB_NAME       database name (default: isureal)
`

type config struct {
	unit        string
	threshold   int
	window      time.Duration
	banDuration time.Duration
	dbHost      string
	dbPort      string
	dbUser      string
	dbPassword  string
	dbName      string
}

func loadConfig(getenv func(string) string) (config, error) {
	get := func(key, defaultValue string) string {
		if value := getenv(key); value != "" {
			return value
		}
		return defaultValue
	}
	cfg := config{
		unit:       get("GUARD_JOURNAL_UNIT", "isureal-*.service"),
		dbHost:     get("ISUREAL_DB_HOST", "127.0.0.1"),
		dbPort:     get("ISUREAL_DB_PORT", "5432"),
		dbUser:     get("ISUREAL_DB_USER", "isureal"),
		dbPassword: get("ISUREAL_DB_PASSWORD", "isureal"),
		dbName:     get("ISUREAL_DB_NAME", "isureal"),
	}
	if _, err := path.Match(cfg.unit, ""); err != nil {
		return config{}, fmt.Errorf("GUARD_JOURNAL_UNIT must be a valid pattern")
	}
	var err error
	if cfg.threshold, err = strconv.Atoi(get("GUARD_FAIL_THRESHOLD", "10")); err != nil || cfg.threshold < 1 {
		return config{}, fmt.Errorf("GUARD_FAIL_THRESHOLD must be a positive integer")
	}
	if cfg.window, err = time.ParseDuration(get("GUARD_FAIL_WINDOW", "10s")); err != nil || cfg.window <= 0 {
		return config{}, fmt.Errorf("GUARD_FAIL_WINDOW must be a positive duration")
	}
	if cfg.banDuration, err = time.ParseDuration(get("GUARD_BAN_DURATION", "2m")); err != nil || cfg.banDuration <= 0 {
		return config{}, fmt.Errorf("GUARD_BAN_DURATION must be a positive duration")
	}
	return cfg, nil
}

func (c config) dsn() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.dbUser, c.dbPassword),
		Host:     net.JoinHostPort(c.dbHost, c.dbPort),
		Path:     "/" + c.dbName,
		RawQuery: "application_name=isureal-guard",
	}
	return u.String()
}

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	logger := log.New(os.Stdout, "", 0)
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Fatal(err)
	}
	logger.Printf("isureal-guard starting: unit=%s threshold=%d window=%s ban_duration=%s db=%s:%s/%s",
		cfg.unit, cfg.threshold, cfg.window, cfg.banDuration, cfg.dbHost, cfg.dbPort, cfg.dbName)

	db, err := sql.Open("pgx", cfg.dsn())
	if err != nil {
		logger.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	journal := exec.CommandContext(ctx, "journalctl", "--follow", "--lines", "0", "--output", "json")
	journal.Stderr = os.Stderr
	stdout, err := journal.StdoutPipe()
	if err != nil {
		logger.Fatal(err)
	}
	if err := journal.Start(); err != nil {
		logger.Fatal(err)
	}
	banner := &dbBanner{db: db, duration: cfg.banDuration}
	err = run(ctx, stdout, cfg.unit, newDetector(cfg.threshold, cfg.window), banner.ban, logger)
	_ = journal.Wait()
	if err != nil && ctx.Err() == nil {
		logger.Fatal(err)
	}
}
