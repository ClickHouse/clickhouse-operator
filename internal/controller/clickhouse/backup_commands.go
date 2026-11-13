package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

const (
	backupOperationStateQuery = `SELECT toString(status), error, num_files, total_size, compressed_size
FROM system.backups
WHERE id = ?`
	settingExistsQuery = `SELECT count() FROM system.settings WHERE name = ?`

	// settingAllowServerCredentials lets a statement use the credentials of the server itself.
	// Servers that do not know the setting allow it unconditionally.
	settingAllowServerCredentials = "s3_allow_server_credentials_in_user_queries" //nolint:gosec // setting name
	// settingSameCredentialsForBase makes RESTORE read the base backups with the credentials of the restored backup.
	settingSameCredentialsForBase = "use_same_s3_credentials_for_base_backup" //nolint:gosec // setting name
	settingBackupID               = "id"
	settingBaseBackup             = "base_backup"

	// codeBadArguments is the ClickHouse BAD_ARGUMENTS error code.
	codeBadArguments = 36
	// duplicateOperationIDMessage is the part of the BAD_ARGUMENTS message returned for an already used operation ID.
	duplicateOperationIDMessage = "with the same ID"

	backupStatusCreating  = "CREATING_BACKUP"
	backupStatusCreated   = "BACKUP_CREATED"
	backupStatusRestoring = "RESTORING"
	backupStatusRestored  = "RESTORED"
)

// bareSettingValue matches the setting values that are rendered without quotes.
var bareSettingValue = regexp.MustCompile(`^(-?[0-9]+(\.[0-9]+)?|true|false)$`)

// s3Location is a backup directory in an S3-compatible storage.
type s3Location struct {
	URL             string
	AccessKeyID     string
	SecretAccessKey string
}

func (l s3Location) hasCredentials() bool {
	return l.AccessKeyID != ""
}

func (l s3Location) sql() string {
	if !l.hasCredentials() {
		return fmt.Sprintf("S3(%s)", quoteString(l.URL))
	}

	return fmt.Sprintf("S3(%s, %s, %s)", quoteString(l.URL), quoteString(l.AccessKeyID), quoteString(l.SecretAccessKey))
}

// backupStatement describes a BACKUP or a RESTORE statement executed on the whole cluster.
type backupStatement struct {
	Restore  bool
	Scope    *v1.BackupScope
	Location s3Location
	// Base is the base backup of an incremental backup.
	Base *s3Location
	// ID identifies the operation in system.backups.
	ID       string
	Settings map[string]string
	// AllowServerCredentials is set when the server requires an explicit permission to use its own credentials.
	AllowServerCredentials bool
}

// SQL renders the statement. It contains the storage credentials and must never be logged.
func (s backupStatement) SQL() string {
	var sb strings.Builder

	if s.Restore {
		sb.WriteString("RESTORE ")
	} else {
		sb.WriteString("BACKUP ")
	}

	sb.WriteString(backupScopeSQL(s.Scope))
	sb.WriteString(" ON CLUSTER ")
	sb.WriteString(quoteString(DefaultClusterName))

	if s.Restore {
		sb.WriteString(" FROM ")
	} else {
		sb.WriteString(" TO ")
	}

	sb.WriteString(s.Location.sql())
	sb.WriteString(" SETTINGS ")
	sb.WriteString(strings.Join(s.settingsSQL(), ", "))
	sb.WriteString(" ASYNC")

	return sb.String()
}

// usesServerCredentials reports whether any of the statement locations is accessed without explicit credentials.
func (s backupStatement) usesServerCredentials() bool {
	return !s.Location.hasCredentials() || (s.Base != nil && !s.Base.hasCredentials())
}

func (s backupStatement) settingsSQL() []string {
	settings := map[string]string{}

	if s.AllowServerCredentials && s.usesServerCredentials() {
		settings[settingAllowServerCredentials] = "1"
	}

	if s.Restore && s.Location.hasCredentials() {
		settings[settingSameCredentialsForBase] = "1"
	}

	for name, value := range s.Settings {
		if bareSettingValue.MatchString(value) {
			settings[name] = value
		} else {
			settings[name] = quoteString(value)
		}
	}

	// Operator managed settings always win over the user provided ones.
	delete(settings, settingBackupID)
	delete(settings, settingBaseBackup)

	rendered := make([]string, 0, len(settings)+2)
	rendered = append(rendered, fmt.Sprintf("%s = %s", settingBackupID, quoteString(s.ID)))

	if s.Base != nil && !s.Restore {
		rendered = append(rendered, fmt.Sprintf("%s = %s", settingBaseBackup, s.Base.sql()))
	}

	for _, name := range slices.Sorted(maps.Keys(settings)) {
		rendered = append(rendered, fmt.Sprintf("%s = %s", name, settings[name]))
	}

	return rendered
}

// backupScopeSQL renders the list of the backup elements. An empty scope covers everything except system tables.
func backupScopeSQL(scope *v1.BackupScope) string {
	if scope == nil || (len(scope.Databases) == 0 && len(scope.Tables) == 0) {
		return "ALL EXCEPT DATABASES system"
	}

	elements := make([]string, 0, len(scope.Databases)+len(scope.Tables))
	for _, database := range scope.Databases {
		elements = append(elements, "DATABASE "+quoteIdentifier(database))
	}

	for _, table := range scope.Tables {
		elements = append(elements, fmt.Sprintf("TABLE %s.%s", quoteIdentifier(table.Database), quoteIdentifier(table.Table)))
	}

	return strings.Join(elements, ", ")
}

var (
	stringEscaper     = strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	identifierEscaper = strings.NewReplacer(`\`, `\\`, "`", "\\`")
)

func quoteString(value string) string {
	return "'" + stringEscaper.Replace(value) + "'"
}

func quoteIdentifier(name string) string {
	return "`" + identifierEscaper.Replace(name) + "`"
}

// backupOperationState is the state of a backup or a restore reported by system.backups.
type backupOperationState struct {
	Status         string
	Error          string
	NumFiles       uint64
	TotalSize      uint64
	CompressedSize uint64
}

func (s backupOperationState) running() bool {
	return s.Status == backupStatusCreating || s.Status == backupStatusRestoring
}

func (s backupOperationState) succeeded() bool {
	return s.Status == backupStatusCreated || s.Status == backupStatusRestored
}

// Ping checks that the replica accepts connections.
func (cmd *commander) Ping(ctx context.Context, id v1.ClickHouseReplicaID) error {
	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping replica %s: %w", id, err)
	}

	return nil
}

// HasSetting reports whether the replica server knows the setting.
func (cmd *commander) HasSetting(ctx context.Context, id v1.ClickHouseReplicaID, name string) (bool, error) {
	conn, err := cmd.getConn(id)
	if err != nil {
		return false, fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	var count uint64
	if err := conn.QueryRow(ctx, settingExistsQuery, name).Scan(&count); err != nil {
		return false, fmt.Errorf("look up setting %s on replica %s: %w", name, id, err)
	}

	return count > 0, nil
}

// StartBackupOperation submits an asynchronous BACKUP or RESTORE statement to the replica.
// A statement with an operation ID that the replica already knows is treated as submitted.
func (cmd *commander) StartBackupOperation(ctx context.Context, id v1.ClickHouseReplicaID, query string) error {
	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	if err := conn.Exec(ctx, query); err != nil {
		if isDuplicateOperationID(err) {
			return nil
		}

		return fmt.Errorf("submit operation to replica %s: %w", id, err)
	}

	return nil
}

// BackupOperationState reads the state of the operation from the replica that executes it.
// It returns false when the replica does not know the operation.
func (cmd *commander) BackupOperationState(
	ctx context.Context,
	id v1.ClickHouseReplicaID,
	operationID string,
) (backupOperationState, bool, error) {
	conn, err := cmd.getConn(id)
	if err != nil {
		return backupOperationState{}, false, fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	var state backupOperationState

	row := conn.QueryRow(ctx, backupOperationStateQuery, operationID)
	if err := row.Scan(&state.Status, &state.Error, &state.NumFiles, &state.TotalSize, &state.CompressedSize); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return backupOperationState{}, false, nil
		}

		return backupOperationState{}, false, fmt.Errorf("query operation %s on replica %s: %w", operationID, id, err)
	}

	return state, true, nil
}

func isDuplicateOperationID(err error) bool {
	var exc *clickhouse.Exception

	return errors.As(err, &exc) && exc.Code == codeBadArguments && strings.Contains(exc.Message, duplicateOperationIDMessage)
}
