package pg

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/samber/oops"
)

// NoticeLevels lists pg.notices.level's accepted values, quietest first.
// Each one logs its own severity and everything more severe.
var NoticeLevels = []string{"off", "warning", "notice", "info", "debug"}

// noticeRank orders Postgres notice severities : higher is more severe.
// An unknown severity ranks as the lowest, which only "debug" lets through.
func noticeRank(severity string) int {
	switch severity {
	case "WARNING":
		return 4
	case "NOTICE":
		return 3
	case "INFO":
		return 2
	}
	return 1 // LOG, DEBUG1..DEBUG5
}

// noticeLevelRank is the lowest noticeRank pg.notices.level lets through ;
// 5 (above every rank) for "off".
func noticeLevelRank(level string) (int, error) {
	switch strings.ToLower(level) {
	case "off", "":
		return 5, nil
	case "warning":
		return 4, nil
	case "notice":
		return 3, nil
	case "info":
		return 2, nil
	case "debug":
		return 1, nil
	}
	return 0, oops.With("value", level).Errorf("pg.notices.level must be one of %s", strings.Join(NoticeLevels, ", "))
}

// Option tunes NewInfosAdminQuery / ReIntrospect.
type Option func(*options)

type options struct {
	notices pgconn.NoticeHandler
	err     error
}

// applyOptions folds opts, failing on the first invalid one.
func applyOptions(opts []Option) (*options, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return o, o.err
}

// WithNoticeLevel logs the notices and warnings Postgres sends on rel's own
// connections (pg.notices.level) : WARNING at warn, NOTICE and INFO at info,
// the rest at debug. Without it, or with "off", they are discarded. An invalid
// level is an error from the constructor.
func WithNoticeLevel(level string) Option {
	return func(o *options) {
		min, err := noticeLevelRank(level)
		if err != nil {
			o.err = err
			return
		}
		if min == 5 {
			return
		}
		o.notices = func(_ *pgconn.PgConn, n *pgconn.Notice) {
			sev := n.SeverityUnlocalized
			if sev == "" {
				sev = n.Severity
			}
			if noticeRank(sev) < min {
				return
			}
			lvl := slog.LevelDebug
			switch noticeRank(sev) {
			case 4:
				lvl = slog.LevelWarn
			case 3, 2:
				lvl = slog.LevelInfo
			}
			attrs := []any{"notice", true, "severity", sev, "sqlstate", n.Code}
			if n.Detail != "" {
				attrs = append(attrs, "detail", n.Detail)
			}
			if n.Hint != "" {
				attrs = append(attrs, "hint", n.Hint)
			}
			if n.Where != "" {
				attrs = append(attrs, "where", n.Where)
			}
			log.Log(context.Background(), lvl, n.Message, attrs...)
		}
	}
}
