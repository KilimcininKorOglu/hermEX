package directory

// Staleness windows for live ActiveSync sessions, in seconds, mirroring the
// reference monitor: a running session is considered gone once it has not been
// updated for sessionRunningTTL; an ended session lingers briefly so it is still
// visible right after finishing, then disappears after sessionEndedTTL.
const (
	sessionRunningTTL = 120
	sessionEndedTTL   = 20
)

// SessionRecord is one live ActiveSync request's telemetry row. Timestamps are
// unix seconds; EndedAt is 0 while the request is still running.
type SessionRecord struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	DeviceID   string `json:"deviceID"`
	DeviceType string `json:"deviceType"`
	UserAgent  string `json:"userAgent"`
	IP         string `json:"ip"`
	Command    string `json:"command"`
	ASVersion  string `json:"asVersion"`
	StartAt    int64  `json:"startAt"`
	LastUpdate int64  `json:"lastUpdate"`
	EndedAt    int64  `json:"endedAt"`
	Push       bool   `json:"push"`
	AddInfo    string `json:"addInfo"`
}

// UpsertSession writes or refreshes a live-session row keyed by ID. The EAS server
// calls it at request start (insert), on activity / each long-poll iteration
// (refreshing last_update and addinfo), and once more with EndedAt set when the
// request finishes. The immutable fields (user/device/ip/start) are kept from the
// initial insert.
func (d *SQLDirectory) UpsertSession(s SessionRecord) error {
	_, err := d.db.Exec(`
		INSERT INTO active_sessions
		  (session_id, username, device_id, device_type, user_agent, ip, command, as_version, start_at, last_update, ended_at, push, addinfo)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		  command = VALUES(command), last_update = VALUES(last_update),
		  ended_at = VALUES(ended_at), push = VALUES(push), addinfo = VALUES(addinfo)`,
		s.ID, s.Username, s.DeviceID, s.DeviceType, s.UserAgent, s.IP, s.Command, s.ASVersion,
		s.StartAt, s.LastUpdate, s.EndedAt, boolToTiny(s.Push), s.AddInfo)
	return err
}

// ListActiveSessions returns the non-stale live sessions as of now (unix seconds):
// running sessions updated within sessionRunningTTL and ended sessions within
// sessionEndedTTL, most-recently-updated first. The age filter is the source of
// truth, so a stale row never shows even before the sweep removes it.
func (d *SQLDirectory) ListActiveSessions(now int64) ([]SessionRecord, error) {
	rows, err := d.db.Query(`
		SELECT session_id, username, device_id, device_type, user_agent, ip, command, as_version,
		       start_at, last_update, ended_at, push, addinfo
		FROM active_sessions
		WHERE (ended_at = 0 AND ? - last_update <= ?) OR (ended_at <> 0 AND ? - ended_at <= ?)
		ORDER BY last_update DESC`,
		now, sessionRunningTTL, now, sessionEndedTTL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var s SessionRecord
		var push int
		if err := rows.Scan(&s.ID, &s.Username, &s.DeviceID, &s.DeviceType, &s.UserAgent, &s.IP,
			&s.Command, &s.ASVersion, &s.StartAt, &s.LastUpdate, &s.EndedAt, &push, &s.AddInfo); err != nil {
			return nil, err
		}
		s.Push = push != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// PurgeStaleSessions deletes aged rows as of now (unix seconds) — ended sessions
// past sessionEndedTTL and running sessions past sessionRunningTTL — and reports
// how many were removed. Best-effort table hygiene; ListActiveSessions already
// hides stale rows by age.
func (d *SQLDirectory) PurgeStaleSessions(now int64) (int64, error) {
	res, err := d.db.Exec(`
		DELETE FROM active_sessions
		WHERE (ended_at <> 0 AND ? - ended_at > ?) OR (ended_at = 0 AND ? - last_update > ?)`,
		now, sessionEndedTTL, now, sessionRunningTTL)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// boolToTiny maps a bool to the 0/1 a TINYINT(1) column stores.
func boolToTiny(b bool) int {
	if b {
		return 1
	}
	return 0
}
